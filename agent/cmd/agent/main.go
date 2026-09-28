// Command endpoint-mgmt-agent is the multi-OS endpoint agent.
//
// Usage:
//
//	endpoint-mgmt-agent -enroll <one-time-token>   # first run: join the fleet
//	endpoint-mgmt-agent                            # subsequent runs: connect
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/enrollment"
	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/inventory"
	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/maintenance"
	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/networkfilter"
	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/patch"
	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/remotecontrol"
	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/remoteexec"
	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/service"
	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/software"
	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/transport"
	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/update"
)

// osProvider is supplied per-OS by the build-tagged package for the target platform.
// We import the platform package anonymously so its init/provider is linked in.
var osProvider = newOSInfoProvider()

func main() {
	var (
		serverURL     string
		enrollToken   string
		credsPath     string
		heartbeatSecs int
		serviceAction string
	)
	flag.StringVar(&serverURL, "server", envOr("AGENT_SERVER", "http://localhost:8443"), "central server URL")
	flag.StringVar(&enrollToken, "enroll", "", "one-time enrollment token (first run only)")
	flag.StringVar(&credsPath, "creds", defaultCredsPath(), "path to persisted credentials")
	flag.IntVar(&heartbeatSecs, "heartbeat", 20, "heartbeat interval in seconds")
	flag.StringVar(&serviceAction, "service", "", "OS service management action (install|uninstall|start|stop|status)")
	flag.Parse()

	if serviceAction != "" {
		handleServiceAction(serviceAction, serverURL, credsPath)
		return
	}

	// Under Windows, the SCM launches us with the arguments recorded at install
	// time, so a launched service must call svc.Run or it exits immediately and
	// the service never reaches RUNNING. runAgent blocks until shutdown, which
	// is exactly the contract the SCM handler expects.
	if service.RunAsService() {
		if err := service.Serve("endpoint-agent", func() error {
			runAgent(serverURL, enrollToken, credsPath, heartbeatSecs)
			return nil
		}); err != nil {
			log.Error().Err(err).Msg("windows service exited with error")
		}
		return
	}

	runAgent(serverURL, enrollToken, credsPath, heartbeatSecs)
}

func runAgent(serverURL, enrollToken, credsPath string, heartbeatSecs int) {
	log.Info().Str("server", serverURL).Str("creds", credsPath).Msg("agent starting")

	creds, err := enrollment.Load(credsPath)
	if enrollToken != "" {
		if err != nil && err != enrollment.ErrNotEnrolled {
			log.Fatal().Err(err).Msg("load existing credentials")
		}
		log.Info().Msg("enrolling with provided token")
		creds, err = enrollment.Exchange(serverURL, enrollToken)
		if err != nil {
			log.Fatal().Err(err).Msg("enrollment failed")
		}
		if err := enrollment.Save(credsPath, creds); err != nil {
			log.Fatal().Err(err).Msg("persist credentials")
		}
		log.Info().Str("device_id", creds.DeviceID).Msg("enrolled successfully")
	} else if err != nil {
		log.Fatal().Err(err).Msg("not enrolled; run with -enroll <token>")
	}

	info, err := osProvider.Collect()
	if err != nil {
		log.Fatal().Err(err).Msg("collect os info")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	collector := newInventoryCollector()

	targetServerURL := creds.ServerURL
	if targetServerURL == "" {
		targetServerURL = serverURL
	}

	// Commands the agent can run. Later modules register more types here.
	dispatcher := transport.NewDispatcher()
	dispatcher.Register("ping", func(ctx context.Context, command, id string, payload json.RawMessage) any {
		return map[string]string{"pong": time.Now().Format(time.RFC3339)}
	})

	// One queue for every package task this device runs, installs and
	// uninstalls alike. Two installers at once collide through MSI's own service
	// and produce failures that read like corrupt packages; an uninstall racing
	// an install of the same product is the same collision, and it is the one an
	// operator triggers by clicking retry on a slow install.
	softwareQueue := software.NewTaskQueue()
	defer softwareQueue.Close()

	dispatcher.Register("software.install", func(ctx context.Context, command, id string, payload json.RawMessage) any {
		var p software.InstallPayload
		// Reject a malformed payload here rather than three goroutines deep, so
		// the command result tells the server the task will never start instead
		// of dispatching something that fails silently.
		if err := json.Unmarshal(payload, &p); err != nil || p.TaskID == "" {
			log.Error().Err(err).Str("task", p.TaskID).Msg("software install payload rejected")
			return map[string]string{"status": "rejected", "error": "invalid install payload"}
		}
		// The install runs on Background, not the command context: that context
		// is cancelled when the command's reply is sent, and an installer needs
		// to outlive the request that started it. The deadline that does apply
		// is installTimeout, set inside the task where the process is started.
		if err := softwareQueue.Submit(p.TaskID, "install", func() {
			defer guardAgentGoroutine("software.install")
			if err := software.ExecuteInstall(context.Background(), targetServerURL, creds.DeviceID, creds.DeviceSecret, payload); err != nil {
				log.Error().Err(err).Str("task", p.TaskID).Msg("software install execution error")
			}
		}); err != nil {
			log.Warn().Err(err).Str("task", p.TaskID).Msg("software install not accepted")
			return map[string]string{"status": "rejected", "error": err.Error()}
		}
		return map[string]string{"status": "dispatched"}
	})

	// Phase 5: Remote Execution & Live Interactive Terminal
	termMgr := remoteexec.NewTerminalManager()
	defer termMgr.CloseAll()

	client := transport.NewClient(creds.ServerURL, creds.DeviceID, creds.DeviceSecret)

	dispatcher.Register("exec.run", func(ctx context.Context, command, id string, payload json.RawMessage) any {
		go func() {
			if err := remoteexec.ExecuteAndReport(context.Background(), targetServerURL, creds.DeviceID, creds.DeviceSecret, payload); err != nil {
				log.Error().Err(err).Msg("remote execution error")
			}
		}()
		return map[string]string{"status": "dispatched"}
	})

	dispatcher.Register("term.open", func(ctx context.Context, command, id string, payload json.RawMessage) any {
		var p struct {
			SessionID string `json:"session_id"`
			Shell     string `json:"shell"`
		}
		_ = json.Unmarshal(payload, &p)
		if p.SessionID == "" {
			p.SessionID = id
		}
		sessID := p.SessionID
		err := termMgr.StartSession(
			sessID,
			p.Shell,
			func(output string) {
				_ = client.Send(transport.Envelope{
					Type: transport.TypeTermData,
					ID:   sessID,
					Payload: map[string]string{
						"session_id": sessID,
						"data":       output,
					},
				})
			},
			func() {
				_ = client.Send(transport.Envelope{
					Type: transport.TypeTermClose,
					ID:   sessID,
					Payload: map[string]string{
						"session_id": sessID,
					},
				})
			},
		)
		if err != nil {
			log.Error().Err(err).Str("session_id", sessID).Msg("start terminal session")
			return map[string]string{"error": err.Error()}
		}
		return map[string]string{"status": "opened"}
	})

	dispatcher.Register("term.data", func(ctx context.Context, command, id string, payload json.RawMessage) any {
		var p struct {
			SessionID string `json:"session_id"`
			Data      string `json:"data"`
		}
		_ = json.Unmarshal(payload, &p)
		if p.SessionID == "" {
			p.SessionID = id
		}
		_ = termMgr.WriteInput(p.SessionID, p.Data)
		return map[string]string{"status": "delivered"}
	})

	dispatcher.Register("term.close", func(ctx context.Context, command, id string, payload json.RawMessage) any {
		var p struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(payload, &p)
		if p.SessionID == "" {
			p.SessionID = id
		}
		_ = termMgr.CloseSession(p.SessionID)
		return map[string]string{"status": "closed"}
	})

	// Phase 6: Patch Management & OS Updates
	patchEngine := patch.NewEngine(targetServerURL, creds.DeviceID, creds.DeviceSecret)

	dispatcher.Register("patch.scan", func(ctx context.Context, command, id string, payload json.RawMessage) any {
		go func() {
			patches, err := patchEngine.Scan(context.Background())
			if err != nil {
				log.Error().Err(err).Msg("patch scan failed")
				return
			}
			if err := patchEngine.ReportScan(context.Background(), patches); err != nil {
				log.Error().Err(err).Msg("report patch scan failed")
			} else {
				log.Info().Int("count", len(patches)).Msg("patch scan reported successfully")
			}
		}()
		return map[string]string{"status": "dispatched"}
	})

	dispatcher.Register("patch.install", func(ctx context.Context, command, id string, payload json.RawMessage) any {
		var params patch.InstallParams
		_ = json.Unmarshal(payload, &params)
		if params.JobID == "" {
			params.JobID = id
		}
		go func() {
			res, err := patchEngine.Install(context.Background(), params)
			if err != nil {
				log.Error().Err(err).Msg("patch installation failed")
				res.Status = "failed"
				res.ErrorMessage = err.Error()
			}
			if err := patchEngine.ReportInstall(context.Background(), res); err != nil {
				log.Error().Err(err).Msg("report patch install result failed")
			} else {
				log.Info().Str("job_id", params.JobID).Str("status", res.Status).Msg("patch install result reported")
			}
		}()
		return map[string]string{"status": "dispatched"}
	})

	// Phase 11: Remote Control
	rcCapturer := remotecontrol.NewPlatformCapturer()
	var activeRCSession *remotecontrol.Session
	var rcMu sync.Mutex

	dispatcher.Register("rc.start", func(ctx context.Context, command, id string, payload json.RawMessage) any {
		var cfg remotecontrol.SessionConfig
		_ = json.Unmarshal(payload, &cfg)
		if cfg.SessionID == "" {
			cfg.SessionID = id
		}

		rcMu.Lock()
		if activeRCSession != nil {
			activeRCSession.Stop()
			activeRCSession = nil
		}
		session := remotecontrol.NewSession(cfg, targetServerURL, remotecontrol.Credentials{
			DeviceID:     creds.DeviceID,
			DeviceSecret: creds.DeviceSecret,
		}, rcCapturer)
		activeRCSession = session
		rcMu.Unlock()

		go func() {
			if err := session.Start(context.Background()); err != nil {
				log.Error().Err(err).Str("session", cfg.SessionID).Msg("remote control session failed")
			}
		}()

		return map[string]string{"status": "starting", "session_id": cfg.SessionID}
	})

	dispatcher.Register("rc.stop", func(ctx context.Context, command, id string, payload json.RawMessage) any {
		rcMu.Lock()
		if activeRCSession != nil {
			activeRCSession.Stop()
			activeRCSession = nil
		}
		rcMu.Unlock()
		return map[string]string{"status": "stopped"}
	})

	// Phase 12: Network & Web Filter
	filterEngine := networkfilter.NewEngine(targetServerURL, creds.DeviceID, creds.DeviceSecret)

	dispatcher.Register("filter.apply", func(ctx context.Context, command, id string, payload json.RawMessage) any {
		var p struct {
			PolicyVersion  string   `json:"policy_version"`
			BlockedDomains []string `json:"blocked_domains"`
		}
		_ = json.Unmarshal(payload, &p)
		if p.PolicyVersion == "" {
			p.PolicyVersion = id
		}

		go func() {
			defer guardAgentGoroutine("networkfilter.apply")
			count, err := filterEngine.ApplyBlockedDomains(p.BlockedDomains)
			status := "synced"
			errMsg := ""
			if err != nil {
				status = "failed"
				errMsg = err.Error()
				log.Error().Err(err).Msg("apply filter rules failed")
			}
			_ = filterEngine.ReportFilterState(context.Background(), p.PolicyVersion, status, count, errMsg)
		}()

		return map[string]string{"status": "applying", "version": p.PolicyVersion}
	})

	// Phase 13: Agent Self-Update & Rollout
	updateEngine := update.NewEngine(targetServerURL, creds.DeviceID, creds.DeviceSecret)

	dispatcher.Register("update.apply", func(ctx context.Context, command, id string, payload json.RawMessage) any {
		var params update.UpdateParams
		_ = json.Unmarshal(payload, &params)
		if params.TaskID == "" {
			params.TaskID = id
		}

		go func() {
			defer guardAgentGoroutine("update.apply")
			if err := updateEngine.ApplyUpdate(context.Background(), params); err != nil {
				log.Error().Err(err).Str("task_id", params.TaskID).Msg("agent self-update failed")
			} else {
				log.Info().Str("task_id", params.TaskID).Str("version", params.TargetVersion).Msg("agent self-update completed successfully")
			}
		}()

		return map[string]string{"status": "dispatched", "task_id": params.TaskID}
	})

	// Phase 15: Device Maintenance
	maintEngine := maintenance.NewEngine(targetServerURL, creds.DeviceID, creds.DeviceSecret)

	dispatcher.Register("maintenance.run", func(ctx context.Context, command, id string, payload json.RawMessage) any {
		var params maintenance.StepRequest
		_ = json.Unmarshal(payload, &params)
		if params.TaskID == "" {
			params.TaskID = id
		}

		// Cheap first gate so the immediate command_result reply is honest.
		// Engine.Run re-checks; do not rely on this one.
		if !maintenance.Allowed(params.TaskType) {
			log.Warn().Str("task_id", params.TaskID).Str("task_type", params.TaskType).
				Msg("rejecting maintenance task with unknown task type")
			return map[string]string{"error": "unsupported task type"}
		}

		go func() {
			defer guardAgentGoroutine("maintenance.run")
			if err := maintEngine.Run(context.Background(), payload); err != nil {
				log.Error().Err(err).Str("task_id", params.TaskID).Msg("maintenance task failed")
			}
		}()

		return map[string]string{"status": "dispatched", "task_id": params.TaskID}
	})

	client.SetCommandHandler(dispatcher.Handle)

	// On-demand collection runs on the client's goroutine; it reports over the
	// same socket the periodic scheduler uses.
	client.SetCollectHandler(func(ctx context.Context) {
		if err := inventory.CollectOnce(ctx, collector, client); err != nil {
			log.Warn().Err(err).Msg("on-demand inventory collect")
		} else {
			log.Info().Msg("inventory collected on server request")
		}
	})

	// Periodic inventory: collect immediately on startup, then on a stable per-
	// device cadence. The scheduler swallows its own errors so inventory never
	// affects command handling.
	go func() {
		sched := inventory.NewScheduler(collector, client, creds.DeviceID,
			inventoryPeriod(), inventoryStaggerWindow())
		sched.Run(ctx)
	}()

	// Advertise what this build can do, so the server avoids sending commands to
	// an agent that would silently drop them.
	client.SetHelloExtra(map[string]any{
		"capabilities": append(inventoryCapabilities(), maintenance.Capabilities()...),
	})

	// On shutdown, close the socket so the server marks the device offline
	// promptly instead of waiting for a ping/pong deadline.
	go func() {
		<-ctx.Done()
		log.Info().Msg("shutdown signal received, closing connection")
		client.Close()
	}()

	if err := client.Run(ctx, info); err != nil && ctx.Err() == nil {
		log.Error().Err(err).Msg("transport ended")
	}
	log.Info().Msg("agent stopped")
}

// guardAgentGoroutine keeps a panic in one background task from taking down the
// whole agent. The agent is a managed OS service, so a crashed process silently
// disappears from the fleet until the SCM restart policy kicks in; recovering
// in place keeps the connection alive and records the failure instead.
func guardAgentGoroutine(task string) {
	if r := recover(); r != nil {
		log.Error().
			Str("task", task).
			Str("panic", fmt.Sprintf("%v", r)).
			Str("stack", string(debug.Stack())).
			Msg("recovered panic in agent background task")
	}
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func defaultCredsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return fmt.Sprintf("%s/.endpoint-mgmt/agent-creds.json", home)
}

// inventoryPeriod is how long between full collections for one device.
// 4 hours is frequent enough to catch a re-provisioned machine during a work
// day, and rare enough that 500 agents collection at once is not a load event.
func inventoryPeriod() time.Duration { return 4 * time.Hour }

// inventoryStaggerWindow is how widely collection start times are spread.
// The scheduler derives a stable per-device offset from this window, so the
// fleet does not align on the hour even on a fresh rollout.
func inventoryStaggerWindow() time.Duration { return 30 * time.Minute }

func handleServiceAction(action, serverURL, credsPath string) {
	cfg := service.DefaultConfig([]string{"-server", serverURL, "-creds", credsPath})
	mgr, err := service.NewManager(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("init service manager")
	}

	switch action {
	case "install":
		if err := mgr.Install(); err != nil {
			log.Fatal().Err(err).Msg("install service failed")
		}
		fmt.Printf("Service '%s' successfully installed as OS daemon.\n", cfg.Name)
	case "uninstall", "remove":
		if err := mgr.Uninstall(); err != nil {
			log.Fatal().Err(err).Msg("uninstall service failed")
		}
		fmt.Printf("Service '%s' successfully removed.\n", cfg.Name)
	case "start":
		if err := mgr.Start(); err != nil {
			log.Fatal().Err(err).Msg("start service failed")
		}
		fmt.Printf("Service '%s' started successfully.\n", cfg.Name)
	case "stop":
		if err := mgr.Stop(); err != nil {
			log.Fatal().Err(err).Msg("stop service failed")
		}
		fmt.Printf("Service '%s' stopped successfully.\n", cfg.Name)
	case "status":
		status, err := mgr.Status()
		if err != nil {
			fmt.Printf("Service '%s' status: %s (query error: %v)\n", cfg.Name, status, err)
		} else {
			fmt.Printf("Service '%s' status: %s\n", cfg.Name, status)
		}
	default:
		log.Fatal().Str("action", action).Msg("unknown service action (use install, uninstall, start, stop, or status)")
	}
}
