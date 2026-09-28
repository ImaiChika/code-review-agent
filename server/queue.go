// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//
// Package server — reviewQueue：M7-F1 异步审查队列。
//
// POST /api/reviews 不再同步执行审查（一次容器沙箱审查可达 30s+，会把所有
// 其他用户的请求一起挂起），而是入队后立即返回 202 + task_id；worker 池
// 消费任务，前端轮询 GET /api/tasks/{id} 取进度与结果。
//
// 状态机：queued → running → completed / failed。
// 进行中状态（queued/running）只存在于内存注册表——review.Run 成功时才写
// cr_review_tasks 行，提前占位会与其 CREATE 冲突；失败（执行出错/超时）由
// 队列侧经 CreateFailedTask 补记，保证失败历史可查。
// 服务重启会丢失未完成任务的内存态（进程退出时审查本身也无法完成），属预期。
package server

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"code-review-agent/report"
	"code-review-agent/review"
	"code-review-agent/storage"
)

// 队列容量与注册表保留上限：提交超过容量返回 503（背压），
// 注册表只保留最近 maxKeptJobs 个任务防止无限增长（仅淘汰已终态的）。
const (
	queueCapacity = 256
	maxKeptJobs   = 1000
)

var (
	// errQueueFull 队列已满（积压超过容量），调用方应返回 503。
	errQueueFull = errors.New("审查队列已满，请稍后重试")
	// errQueueClosed 服务正在关闭，不再接受新任务。
	errQueueClosed = errors.New("服务正在关闭，暂不接受新审查")
)

// queuedJob 队列中的一个审查任务（含内存态状态机）。
type queuedJob struct {
	id         string
	inputType  string // diff_content / repo_path（占位展示用）
	inputPath  string
	submitted  time.Time
	opts       review.Options
	runFn      func(review.Options) (*report.ReviewReport, error) // 每任务可覆盖（测试注入）
	finishHook func(status, errMsg string)                        // 状态迁移回调（测试注入）

	mu     sync.Mutex
	status string // queued / running / completed / failed
	errMsg string
}

func (j *queuedJob) getStatus() (string, string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status, j.errMsg
}

func (j *queuedJob) setStatus(status, errMsg string) {
	j.mu.Lock()
	j.status = status
	j.errMsg = errMsg
	j.mu.Unlock()
	if j.finishHook != nil {
		j.finishHook(status, errMsg)
	}
}

// reviewQueue 异步审查队列：worker 池 + 任务注册表。
type reviewQueue struct {
	runFn   func(review.Options) (*report.ReviewReport, error) // 默认 review.Run（测试可注入）
	timeout time.Duration                                      // 单任务看门狗上限
	work    chan *queuedJob
	quit    chan struct{}
	wg      sync.WaitGroup
	store   storage.Store

	mu     sync.Mutex
	closed bool
	jobs   map[string]*queuedJob
	order  []string // 提交顺序，用于淘汰最老的已终态任务
}

// newReviewQueue 启动 worker 池。workers < 1 时取 1（默认串行，与全局互斥时期
// 的 SQLite 写语义一致，只是 HTTP 不再被阻塞）；timeout <= 0 时取 10 分钟。
func newReviewQueue(store storage.Store, workers int, timeout time.Duration) *reviewQueue {
	if workers < 1 {
		workers = 1
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	q := &reviewQueue{
		runFn:   review.Run,
		timeout: timeout,
		work:    make(chan *queuedJob, queueCapacity),
		quit:    make(chan struct{}),
		store:   store,
		jobs:    make(map[string]*queuedJob),
	}
	for i := 0; i < workers; i++ {
		q.wg.Add(1)
		go q.worker()
	}
	return q
}

// submit 入队一个任务；job.status 初始为 queued。
func (q *reviewQueue) submit(job *queuedJob) error {
	job.status = "queued"

	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return errQueueClosed
	}
	q.jobs[job.id] = job
	q.order = append(q.order, job.id)
	q.pruneLocked()
	q.mu.Unlock()

	select {
	case q.work <- job:
		return nil
	default:
		return errQueueFull
	}
}

// pruneLocked 控制注册表规模：超过 maxKeptJobs 时从最老的开始淘汰已终态任务。
func (q *reviewQueue) pruneLocked() {
	for len(q.order) > maxKeptJobs {
		oldest := q.order[0]
		if st, _ := q.jobs[oldest].getStatus(); st == "queued" || st == "running" {
			break // 最老的还在执行，不淘汰
		}
		q.order = q.order[1:]
		delete(q.jobs, oldest)
	}
}

// lookup 查内存注册表（进行中任务 / 刚终态但尚未被查询的任务）。
func (q *reviewQueue) lookup(taskID string) *queuedJob {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.jobs[taskID]
}

// Close 停止接收新任务并等待在跑任务收尾（最多 3s，超时放弃等待）。
func (q *reviewQueue) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	close(q.quit)
	done := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
}

func (q *reviewQueue) worker() {
	defer q.wg.Done()
	for {
		select {
		case <-q.quit:
			return
		case job := <-q.work:
			q.execute(job)
		}
	}
}

// execute 执行一个任务：runFn 在独立 goroutine 里跑，主循环看门狗限时。
// 超时后任务标记 failed 并补记 DB 行；若 runFn 此后才完成，其落库 INSERT 会
// 因任务行已存在而失败，最终状态保持 failed（超时本身意味着有异常，可接受）。
func (q *reviewQueue) execute(job *queuedJob) {
	job.setStatus("running", "")

	run := job.runFn
	if run == nil {
		run = q.runFn
	}

	type runResult struct {
		rep *report.ReviewReport
		err error
	}
	done := make(chan runResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- runResult{nil, fmt.Errorf("审查执行 panic: %v", r)}
			}
		}()
		rep, err := run(job.opts)
		done <- runResult{rep, err}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			job.setStatus("failed", r.err.Error())
			q.persistFailure(job, r.err.Error())
		} else {
			job.setStatus("completed", "")
		}
	case <-time.After(q.timeout):
		msg := fmt.Sprintf("任务执行超时（上限 %s）", q.timeout)
		job.setStatus("failed", msg)
		q.persistFailure(job, msg)
	}
}

// persistFailure 把异步失败（执行出错/超时）补记进 cr_review_tasks。
// DB 写失败不影响内存态（注册表里仍有失败原因），只打日志。
func (q *reviewQueue) persistFailure(job *queuedJob, errMsg string) {
	task := &storage.ReviewTask{
		TaskID:    job.id,
		Status:    storage.TaskStatusFailed,
		InputType: job.inputType,
		InputPath: job.inputPath,
		StartedAt: job.submitted,
	}
	if err := q.store.CreateFailedTask(task, errMsg); err != nil {
		fmt.Printf("⚠️ 补记失败任务 %s 时出错: %v\n", job.id, err)
	}
}
