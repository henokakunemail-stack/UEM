// Command endpoint-mgmt-agent is the multi-OS endpoint agent.
//
// Usage:
//
//	endpoint-mgmt-agent -enroll <one-time-token>   # first run: join the fleet
//	endpoint-mgmt-agent                            # subsequent runs: connect
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/enrollment"
	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/inventory"
	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/maintenance"
	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/networkfilter"
	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo"
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
		rcWorkerArg   string
		rcWorkerCreds string
	)
	flag.StringVar(&serverURL, "server", envOr("AGENT_SERVER", "http://localhost:8443"), "central server URL")
	flag.StringVar(&enrollToken, "enroll", "", "one-time enrollment token (first run only)")
	flag.StringVar(&credsPath, "creds", defaultCredsPath(), "path to persisted credentials")
	flag.IntVar(&heartbeatSecs, "heartbeat", 20, "heartbeat interval in seconds")
	flag.StringVar(&serviceAction, "service", "", "OS service management action (install|uninstall|start|stop|status)")
	// Internal: the remote control session worker launched into the logged-on
	// user's session. Not an operator-facing flag — see runRCWorker.
	flag.StringVar(&rcWorkerArg, "rc-worker", "", "internal: base64 remote control session config (session worker mode)")
	flag.StringVar(&rcWorkerCreds, "rc-creds", "", "internal: credential path for the session worker")
	flag.Parse()

	// The worker branch runs before the service check on purpose. A worker spawned
	// into the user's session is not a service, so RunAsService would be false
	// anyway — but ordering it first also means a misconfigured worker can never be
	// mistaken for the daemon and end up enrolling a second device.
	if rcWorkerArg != "" {
		runRCWorker(rcWorkerArg, rcWorkerCreds)
		return
	}

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

// runRCWorker is the whole of the session worker's behaviour: run one remote
// control session in the interactive user session, then exit.
//
// It deliberately does almost nothing else. The service already owns enrollment,
// the heartbeat, inventory and command dispatch; a second copy of all of it
// would fight the service for the credentials file and register the device
// twice. The worker exists for exactly one reason: to be on a desktop.
//
// The session here is the ordinary remotecontrol.Session, dialling the ordinary
// relay with the ordinary credentials. Nothing in the capture, relay or frame
// path is worker-aware, and nothing needed to be: running in the user's session
// is sufficient, because that is what makes GetDC and SendInput point at a
// desktop and a human.
func runRCWorker(cfgArg, credsPath string) {
	raw, err := base64.StdEncoding.DecodeString(cfgArg)
	if err != nil {
		log.Error().Err(err).Msg("decode rc worker config")
		return
	}
	var cfg remotecontrol.SessionConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		log.Error().Err(err).Msg("parse rc worker config")
		return
	}

	creds, err := enrollment.Load(credsPath)
	if err != nil {
		// The secret was never passed on the command line, so this is the only
		// way the worker can authenticate. Without it there is no session, and
		// saying so here is the only trace: the service already logged that it
		// handed the session off.
		log.Error().Err(err).Str("creds", credsPath).Msg("rc worker cannot load credentials")
		return
	}

	serverURL := creds.ServerURL
	if serverURL == "" {
		log.Error().Msg("rc worker has no server url in its credentials")
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	session := remotecontrol.NewSession(cfg, serverURL, remotecontrol.Credentials{
		DeviceID:     creds.DeviceID,
		DeviceSecret: creds.DeviceSecret,
	}, remotecontrol.NewPlatformCapturer())

	if err := session.Start(ctx); err != nil {
		log.Error().Err(err).Str("session", cfg.SessionID).Msg("rc worker session failed to start")
		return
	}
	log.Info().Str("session", cfg.SessionID).Msg("rc worker session started on the interactive desktop")

	// Block until the session ends or the operator stops it. Start returns as
	// soon as the socket is up and the loops are running, so returning early here
	// would end the session the instant it began.
	session.Wait(ctx)
	log.Info().Str("session", cfg.SessionID).Msg("rc worker session ended")
}

func runAgent(serverURL, enrollToken, credsPath string, heartbeatSecs int) {
	log.Info().Str("server", serverURL).Str("creds", credsPath).Msg("agent starting")

	creds, err := enrollment.Load(credsPath)
	if enrollToken != "" {
		if err != nil && !errors.Is(err, enrollment.ErrNotEnrolled) {
			log.Fatal().Err(err).Msg("load existing credentials")
		}
		log.Info().Msg("enrolling with provided token")
		newCreds, err := enrollment.Exchange(serverURL, enrollToken)
		if err != nil {
			log.Fatal().Err(err).Msg("enrollment failed")
		}
		// A visible credential store that already points at a different device
		// would be silently swapped for a new identity. Refuse unless the
		// server returned the same device the machine already is.
		if creds.DeviceID != "" && newCreds.DeviceID != creds.DeviceID {
			log.Fatal().Str("stored", creds.DeviceID).Str("returned", newCreds.DeviceID).
				Msg("refusing to overwrite credentials for a different device")
		}
		creds = newCreds
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

	dispatcher.Register("software.uninstall", func(ctx context.Context, command, id string, payload json.RawMessage) any {
		var p software.UninstallPayload
		if err := json.Unmarshal(payload, &p); err != nil || p.TaskID == "" {
			log.Error().Err(err).Str("task", p.TaskID).Msg("software uninstall payload rejected")
			return map[string]string{"status": "rejected", "error": "invalid uninstall payload"}
		}
		// Queued behind installs on purpose: see softwareQueue. An uninstall
		// racing an install of the same product is the MSI collision, and it is
		// the one an operator triggers by clicking retry on a slow install.
		if err := softwareQueue.Submit(p.TaskID, "uninstall", func() {
			defer guardAgentGoroutine("software.uninstall")
			if err := software.ExecuteUninstall(context.Background(), targetServerURL, creds.DeviceID, creds.DeviceSecret, payload); err != nil {
				log.Error().Err(err).Str("task", p.TaskID).Msg("software uninstall execution error")
			}
		}); err != nil {
			log.Warn().Err(err).Str("task", p.TaskID).Msg("software uninstall not accepted")
			return map[string]string{"status": "rejected", "error": err.Error()}
		}
		return map[string]string{"status": "dispatched"}
	})

	// Phase 5: Remote Execution & Live Interactive Terminal
	termMgr := remoteexec.NewTerminalManager()
	defer termMgr.CloseAll()

	client := transport.NewClient(creds.ServerURL, creds.DeviceID, creds.DeviceSecret)

	// Uninstall a program named off a device's installed-software list, with no
	// task row and no arguments from the operator.
	//
	// It replies with transport.Deferred rather than an immediate result because
	// the reply carries the outcome, not the acceptance. Answering "dispatched"
	// here would write status='done' to the command row while the uninstall was
	// still waiting behind an installer in softwareQueue -- and if the agent were
	// stopped before the queue reached it, the row would claim a removal that
	// never happened. The result goes out once the work is really finished.
	//
	// Registered after client is built because the queued work sends its own
	// reply; the two handlers above it do not, and do not need to.
	dispatcher.Register("software.uninstall.by_name", func(ctx context.Context, command, id string, payload json.RawMessage) any {
		var p struct {
			SoftwareName string `json:"software_name"`
		}
		if err := json.Unmarshal(payload, &p); err != nil || strings.TrimSpace(p.SoftwareName) == "" {
			log.Error().Err(err).Str("command", id).Msg("software uninstall by name payload rejected")
			return map[string]string{"status": "rejected", "error": "invalid uninstall payload: software_name is required"}
		}
		name := strings.TrimSpace(p.SoftwareName)

		// Queued behind installs on purpose: see softwareQueue. An uninstall
		// racing an install of the same product is the MSI collision.
		if err := softwareQueue.Submit(id, "uninstall_by_name", func() {
			defer guardAgentGoroutine("software.uninstall.by_name")

			// Background, not the command context: that context is cancelled when
			// the command's reply is sent, and no reply is sent from in here. The
			// deadline is the same 20 minutes the catalog uninstall path uses, set
			// where the work starts rather than borrowed from the command.
			runCtx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
			defer cancel()

			exitCode, output, runErr := software.UninstallByName(runCtx, name)
			result := software.DescribeUninstall(name, exitCode, output, runErr)

			// ErrNotInstalled is a completed check, not a failure: a machine that
			// never had the program already satisfies the rule being enforced. It
			// reports done with the reason in the result text.
			status := "done"
			switch {
			case runErr == nil:
				log.Info().Str("software", name).Int("exit_code", exitCode).Msg("uninstall by name finished")
			case errors.Is(runErr, software.ErrNotInstalled):
				log.Info().Str("software", name).Msg("uninstall by name: target was not installed")
			default:
				status = "failed"
				log.Warn().Str("software", name).Int("exit_code", exitCode).
					Err(runErr).Msg("uninstall by name refused or failed")
			}

			// Reported even on failure. This row is the only record the console has
			// of what happened on the endpoint, and the refusals -- no quiet command,
			// no elevation, ambiguous name -- arrive here and nowhere else. A
			// dropped send leaves the row at 'sent', which says the agent did not
			// finish rather than claiming a removal that never ran.
			_ = client.Send(transport.Envelope{
				Type:   transport.TypeCommandResult,
				ID:     id,
				Status: status,
				Result: map[string]string{"result": result},
			})
		}); err != nil {
			log.Warn().Err(err).Str("command", id).Msg("software uninstall by name not accepted")
			return map[string]string{"status": "rejected", "error": err.Error()}
		}
		return transport.Deferred{}
	})

	dispatcher.Register("exec.run", func(ctx context.Context, command, id string, payload json.RawMessage) any {
		go func() {
			if err := remoteexec.ExecuteAndReport(context.Background(), targetServerURL, creds.DeviceID, creds.DeviceSecret, payload); err != nil {
				log.Error().Err(err).Msg("remote execution error")
			}
		}()
		return map[string]string{"status": "dispatched"}
	})

	dispatcher.Register("exec.cancel", func(ctx context.Context, command, id string, payload json.RawMessage) any {
		var p struct {
			ExecutionID string `json:"execution_id"`
		}
		_ = json.Unmarshal(payload, &p)
		if p.ExecutionID == "" {
			p.ExecutionID = id
		}
		remoteexec.HandleCancel(p.ExecutionID)
		return map[string]string{"status": "cancel_dispatched"}
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
		// Not "delivered" regardless of what happened. WriteInput answers
		// "terminal session not found" or "terminal session is closed" when the
		// shell has already exited -- which is what happens whenever the operator
		// starts typing into a session that died between the click and the
		// keystroke. Discarding that error told the console the keystroke arrived
		// when nothing was written, and the operator's only symptom was a shell
		// that had stopped responding. Interactive input is the one place where
		// silent loss is felt immediately and cannot be reproduced afterwards.
		if err := termMgr.WriteInput(p.SessionID, p.Data); err != nil {
			log.Warn().Err(err).Str("session_id", p.SessionID).
				Msg("terminal input dropped: session is not writable")
			return map[string]string{"status": "rejected", "error": err.Error()}
		}
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
		// CloseSession is already nil-on-missing on purpose -- closing a session that
		// has ended is a no-op the caller should not have to distinguish. So "closed"
		// is honest here and was already; the input path above is the one that had to
		// report an error, because failing to write is not a no-op.
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
	// activeRCWorker is the pid of a session worker this service spawned into
	// the user's session. It is tracked separately from activeRCSession because
	// the two are mutually exclusive: when a worker owns the session there is no
	// local Session object, and stopping a local nil pointer would silently
	// leave the worker's process running and streaming to nobody.
	var activeRCWorker uint32
	var rcMu sync.Mutex

	dispatcher.Register("rc.start", func(ctx context.Context, command, id string, payload json.RawMessage) any {
		var cfg remotecontrol.SessionConfig
		// A payload that does not decode is not a session to start. Discarding
		// the error and carrying on produced a session with no id, which dialed
		// the relay's root path and then failed to attach: the console waited on
		// a desktop that could never arrive, with nothing on either side saying
		// why. The server cannot do anything with a refusal either, so the reason
		// goes into the log where an operator looking at the agent can find it.
		if err := json.Unmarshal(payload, &cfg); err != nil {
			log.Error().Err(err).Str("session", id).Msg("decode rc.start payload")
			return map[string]string{"status": "rejected", "error": "invalid remote control request"}
		}
		if cfg.SessionID == "" {
			cfg.SessionID = id
		}
		if cfg.RelayURL == "" {
			return map[string]string{"status": "rejected", "error": "remote control request carried no relay url"}
		}

		rcMu.Lock()
		if activeRCSession != nil {
			activeRCSession.Stop()
			activeRCSession = nil
		}
		if activeRCWorker != 0 {
			_ = remotecontrol.KillSessionWorker(activeRCWorker)
			activeRCWorker = 0
		}
		session := remotecontrol.NewSession(cfg, targetServerURL, remotecontrol.Credentials{
			DeviceID:     creds.DeviceID,
			DeviceSecret: creds.DeviceSecret,
		}, rcCapturer)
		activeRCSession = session
		rcMu.Unlock()

		// Hand the session to a process on the user's desktop before giving up on
		// it here. This service runs in Session 0, which has no desktop at all:
		// GetDC cannot blit from it and SendInput cannot reach a human through it,
		// so a session started here can never carry a frame. The worker is the
		// same binary re-run in the logged-on user's session, which is what makes
		// the ordinary capture path work without a single change to it.
		//
		// Failing to spawn is not fatal and must not be silent: a machine with
		// nobody logged on has no session to attach to, and in that case falling
		// back below is correct — the console gets an explanation instead of a
		// connection that never produces a frame.
		if exe, err := os.Executable(); err == nil {
			pid, err := remotecontrol.SpawnSessionWorker(exe, credsPath, cfg)
			if err == nil {
				log.Info().Uint32("worker_pid", pid).Str("session", cfg.SessionID).
					Msg("remote control session handed to the user session worker")
				rcMu.Lock()
				activeRCWorker = pid
				activeRCSession = nil
				rcMu.Unlock()
				return map[string]string{"status": "starting", "session_id": cfg.SessionID}
			}
			log.Warn().Err(err).Str("session", cfg.SessionID).
				Msg("could not start a session worker; falling back to the service process")
		} else {
			log.Warn().Err(err).Msg("cannot locate the agent executable for a session worker")
		}

		go func() {
			if err := session.Start(context.Background()); err != nil {
				log.Error().Err(err).Str("session", cfg.SessionID).Msg("remote control session failed")
				// Tell the console why the desktop is not coming. Without it the
				// operator watches a relay that is open and connected and has
				// nothing else to go on, which is the state this whole path was
				// supposed to rule out.
				session.SendNotice("connect_failed", err.Error())
			}
		}()

		return map[string]string{"status": "starting", "session_id": cfg.SessionID}
	})

	dispatcher.Register("rc.stop", func(ctx context.Context, command, id string, payload json.RawMessage) any {
		var p struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(payload, &p)
		if p.SessionID == "" {
			p.SessionID = id
		}

		rcMu.Lock()
		defer rcMu.Unlock()
		// Only the session named by the command is stopped. Stopping whichever
		// session happened to be active would tear down a live desktop because a
		// stop for a session that had already ended arrived late.
		if activeRCSession != nil && activeRCSession.ID() == p.SessionID {
			activeRCSession.Stop()
			activeRCSession = nil
		}
		// A worker has no Session object to stop, so the process is the session.
		// Leaving it alive would keep a desktop streaming to a console that has
		// already gone away, on an endpoint whose operator believes they ended
		// it. A stale pid is normal — it means the worker exited on its own — and
		// is logged rather than treated as a failure.
		if activeRCWorker != 0 {
			if err := remotecontrol.KillSessionWorker(activeRCWorker); err != nil {
				log.Warn().Err(err).Uint32("worker_pid", activeRCWorker).
					Msg("session worker was already gone")
			}
			activeRCWorker = 0
		}
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
			count, degraded, err := filterEngine.ApplyBlockedDomains(p.BlockedDomains)
			status, errMsg := "synced", ""
			switch {
			case err != nil:
				status, errMsg = "failed", err.Error()
				log.Error().Err(err).Msg("apply filter rules failed")
			case degraded != "":
				// Not 'failed': the rules did reach the device. But the firewall layer
				// was refused or some domains did not resolve, so the hosts file alone
				// is standing and subdomains of those domains are still reachable.
				// Reporting 'synced' here is how an operator concludes the endpoint is
				// enforcing web filtering when it is not.
				status, errMsg = "degraded", degraded
				log.Warn().Str("reason", degraded).Msg("filter policy applied with reduced enforcement")
			}
			_ = filterEngine.ReportFilterState(context.Background(), p.PolicyVersion, status, count, errMsg)
		}()

		return map[string]string{"status": "applying", "version": p.PolicyVersion}
	})

	// Phase 13: Agent Self-Update & Rollout
	updateEngine := update.NewEngine(targetServerURL, creds.DeviceID, creds.DeviceSecret)
	// The key this agent verifies release manifests against resolves now, not at
	// the first update: a misconfiguration surfaces in the startup log beside
	// everything else instead of inside a task an operator dispatches later.
	updateEngine.InitTrust(context.Background(), osinfo.Version)

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
		if err := json.Unmarshal(payload, &params); err != nil {
			log.Error().Err(err).Msg("decode maintenance.run payload")
			return map[string]string{"error": "invalid maintenance request"}
		}

		// Cheap first gate so the immediate command_result reply is honest.
		// Engine.Run re-checks; do not rely on this one.
		if !maintenance.Allowed(params.TaskType) {
			log.Warn().Str("task_id", id).Str("task_type", params.TaskType).
				Msg("rejecting maintenance task with unknown task type")
			return map[string]string{"error": "unsupported task type"}
		}

		go func() {
			defer guardAgentGoroutine("maintenance.run")
			// id, not the locally patched params: Run resolves the fallback
			// itself, and passing the raw payload alone left the task_id out
			// of it whenever the server omitted one.
			if err := maintEngine.Run(context.Background(), payload, id); err != nil {
				log.Error().Err(err).Str("task_id", id).Msg("maintenance task failed")
			}
		}()

		return map[string]string{"status": "dispatched", "task_id": id}
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

	heartbeat := time.Duration(heartbeatSecs) * time.Second
	if heartbeat <= 0 {
		heartbeat = transport.DefaultHeartbeat
	}
	if err := client.Run(ctx, info, heartbeat); err != nil && ctx.Err() == nil {
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
