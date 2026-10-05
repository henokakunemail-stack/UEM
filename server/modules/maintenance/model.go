package maintenance

import "time"

// Task types. These are the keys the console sends and the keys the agent
// switches on, so they are defined once here and mirrored — with a compile-time
// check on the agent side — rather than spelled as bare strings at every hop.
const (
	TaskCleanupTemp    = "cleanup_temp"
	TaskDiskCheck      = "disk_check"
	TaskMemoryHygiene  = "memory_hygiene"
	TaskFullScan       = "full_scan"
	TaskLogMaintenance = "log_maintenance"
	TaskServiceCleanup = "service_cleanup"
)

// TaskOrder is the order the console renders the task chooser in. Disk check
// sits after cleanup because it is the one operation that can be slow, and an
// operator scanning for the risky one should not have to hunt for it.
var TaskOrder = []string{
	TaskCleanupTemp,
	TaskDiskCheck,
	TaskMemoryHygiene,
	TaskLogMaintenance,
	TaskServiceCleanup,
	TaskFullScan,
}

// FullScanStepOrder is the order the agent runs a full_scan's steps in. It is
// deliberately not TaskOrder: that is a UI ordering, this is the execution
// sequence, and the two differ (disk check runs last here, but is listed second
// for the console).
var FullScanStepOrder = []string{
	TaskCleanupTemp,
	TaskMemoryHygiene,
	TaskLogMaintenance,
	TaskDiskCheck,
}

// StepsForTaskType returns the ordered steps a task of this type reports. Every
// type except full_scan is a single step, which is the same as itself.
func StepsForTaskType(taskType string) []string {
	if taskType == TaskFullScan {
		return FullScanStepOrder
	}
	return []string{taskType}
}

// StepIndex returns the position of step within the task type's sequence, or -1
// if the step does not belong to that task type at all. A negative result means
// the agent reported a step the task could never have produced.
func StepIndex(taskType, step string) int {
	for i, s := range StepsForTaskType(taskType) {
		if s == step {
			return i
		}
	}
	return -1
}

type TaskInfo struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	// Disruptive is true when the operation may interrupt a logged-in user or
	// needs a reboot to take full effect. The console must show a confirmation
	// for these — silently rebooting a user's laptop is not acceptable.
	Disruptive bool `json:"disruptive"`
}

var TaskCatalog = map[string]TaskInfo{
	TaskCleanupTemp: {
		ID:          TaskCleanupTemp,
		Label:       "Temp & Cache Cleanup",
		Description: "Clears user temp folders, OS temp caches, package-manager caches, the recycle bin, and Windows Update download cache. Recovers disk space without touching user documents.",
		Disruptive:  false,
	},
	TaskDiskCheck: {
		ID:          TaskDiskCheck,
		Label:       "Disk Check & Optimize",
		Description: "Online filesystem scan and TRIM/SSD optimization. On Windows this is chkdsk /scan, which runs while the disk is in use and does not require a reboot. Linux runs fstrim on SSDs.",
		Disruptive:  false,
	},
	TaskMemoryHygiene: {
		ID:          TaskMemoryHygiene,
		Label:       "Memory Hygiene",
		Description: "Trims the standby memory list on Windows, and compacts the page cache and zram swap on Linux. Frees RAM that the kernel is holding but nothing is using.",
		Disruptive:  false,
	},
	TaskLogMaintenance: {
		ID:          TaskLogMaintenance,
		Label:       "Log & Crash Dump Cleanup",
		Description: "Removes rotated logs, stale crash dumps and old package-manager archives. Useful before handing a machine back or a forensic copy. Rotated logs only — the active log is left alone.",
		Disruptive:  false,
	},
	TaskServiceCleanup: {
		ID:          TaskServiceCleanup,
		Label:       "Stale Service & Orphan Cleanup",
		Description: "Reports services and scheduled tasks that appear orphaned or are stuck in a stopped/stopped-disabled state, and clears orphaned install directories left by previous software deployments.",
		Disruptive:  false,
	},
	TaskFullScan: {
		ID:          TaskFullScan,
		Label:       "Full Health Scan",
		Description: "Runs temp cleanup, memory hygiene, log maintenance and an online disk check in sequence, and returns one combined result. The usual choice for a routine sweep.",
		Disruptive:  false,
	},
}

type TargetType string

const (
	TargetDevice TargetType = "device"
	TargetGroup  TargetType = "group"
	TargetAll    TargetType = "all"
)

const (
	JobStatusRunning   = "running"
	JobStatusCompleted = "completed"
	JobStatusPartial   = "partial"
	JobStatusFailed    = "failed"
)

const (
	TaskStatusPending    = "pending"
	TaskStatusDispatched = "dispatched"
	TaskStatusRunning    = "running"
	TaskStatusCompleted  = "completed"
	TaskStatusFailed     = "failed"
	TaskStatusSkipped    = "skipped"
)

type Job struct {
	ID          string     `json:"id" db:"id"`
	Name        string     `json:"name" db:"name"`
	TaskType    string     `json:"task_type" db:"task_type"`
	TargetType  string     `json:"target_type" db:"target_type"`
	TargetID    string     `json:"target_id" db:"target_id"`
	CreatedBy   string     `json:"created_by" db:"created_by"`
	TotalTasks  int        `json:"total_tasks" db:"total_tasks"`
	Dispatched  int        `json:"dispatched" db:"dispatched"`
	Skipped     int        `json:"skipped" db:"skipped"`
	Completed   int        `json:"completed" db:"completed"`
	Failed      int        `json:"failed" db:"failed"`
	Status      string     `json:"status" db:"status"`
	StartedAt   time.Time  `json:"started_at" db:"started_at"`
	CompletedAt *time.Time `json:"completed_at" db:"completed_at"`
}

type Task struct {
	ID             string     `json:"id" db:"id"`
	JobID          string     `json:"job_id" db:"job_id"`
	DeviceID       string     `json:"device_id" db:"device_id"`
	Hostname       string     `json:"hostname" db:"hostname"`
	TaskType       string     `json:"task_type" db:"task_type"`
	Status         string     `json:"status" db:"status"`
	Step           string     `json:"step" db:"step"`
	ExitCode       *int       `json:"exit_code" db:"exit_code"`
	OutputLog      *string    `json:"output_log" db:"output_log"`
	ErrorMessage   *string    `json:"error_message" db:"error_message"`
	RebootRequired bool       `json:"reboot_required" db:"reboot_required"`
	BytesFreed     int64      `json:"bytes_freed" db:"bytes_freed"`
	StartedAt      *time.Time `json:"started_at" db:"started_at"`
	CompletedAt    *time.Time `json:"completed_at" db:"completed_at"`
	CreatedAt      time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at" db:"updated_at"`
}

// RunRequest is the console's POST body. TargetID is required for target_type
// 'device' and 'group', and ignored for 'all'.
type RunRequest struct {
	TaskType   string     `json:"task_type"`
	TargetType TargetType `json:"target_type"`
	TargetID   string     `json:"target_id"`
	Name       string     `json:"name"`
}

// StepReport is what the agent posts back for a single step of a task. A
// multi-step task (full_scan) posts once per step, each with its own status.
type StepReport struct {
	TaskID   string `json:"task_id"`
	Step     string `json:"step"`
	Status   string `json:"status"`
	ExitCode *int   `json:"exit_code,omitempty"`
	// Heartbeat marks a report that exists only to prove the agent is still
	// working. It must be non-terminal and carries no result, and RecordStep
	// advances nothing but updated_at for it — the column the orphan sweep
	// reads. A step that took longer than the sweep's grace window would
	// otherwise be reaped while its agent was demonstrably still running.
	Heartbeat      bool    `json:"heartbeat,omitempty"`
	OutputLog      *string `json:"output_log,omitempty"`
	ErrorMessage   *string `json:"error_message,omitempty"`
	RebootRequired bool    `json:"reboot_required,omitempty"`
	BytesFreed     int64   `json:"bytes_freed,omitempty"`
}
