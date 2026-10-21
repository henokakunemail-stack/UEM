// The install commands the enrollment dialog shows for a freshly minted token.
//
// None of them passes a site: the site chosen in the dialog is stored with the
// token (or the pre-registered device) and the server applies it. Passing it
// again as -site only made the command depend on the agent knowing that flag,
// and an agent built before it existed exits at once on an unknown flag.
//
// Every command enrolls with -enroll-only (or the installer that does), so
// running it again on an enrolled machine keeps that machine's identity
// instead of spending another use of a multi-device token.

export interface EnrollCommand {
  id: string
  labelKey: string
  text: string
}

/** Single-quotes a value for a POSIX shell command line. */
export function shellQuote(v: string): string {
  return `'${v.replace(/'/g, "'\\''")}'`
}

export function enrollCommands(server: string, token: string): EnrollCommand[] {
  return [
    {
      // The installer enrolls, registers the service and starts it; /S makes it
      // usable from GPO or a deployment tool.
      id: 'windows-installer',
      labelKey: 'enroll.cmd_windows_installer',
      text: `EndpointAgent-Setup.exe /S /SERVER=${server} /TOKEN=${token}`,
    },
    {
      // For a machine that already has the agent installed. PowerShell syntax:
      // %ALLUSERSPROFILE% is not expanded there, and a bare endpoint-agent.exe
      // only resolves from the install folder.
      id: 'windows-cli',
      labelKey: 'enroll.cmd_windows_cli',
      text:
        `& "$env:ProgramFiles\\EndpointAgent\\endpoint-agent.exe" -server ${server} -enroll ${token} ` +
        `-enroll-only -creds "$env:ProgramData\\EndpointAgent\\creds.json"; Restart-Service endpoint-agent`,
    },
    {
      // Downloads the agent from this server, checks it, enrolls and installs
      // the systemd service: nothing to copy to the machine first.
      id: 'linux',
      labelKey: 'enroll.linux_one_line',
      text: `curl -fsSL ${server}/install/linux.sh | sudo bash -s -- --server ${shellQuote(server)} --token ${token}`,
    },
    {
      // The macOS package's LaunchDaemon reads /etc/endpoint-agent/creds.json.
      id: 'macos',
      labelKey: 'enroll.cmd_macos',
      text: `sudo endpoint-agent -server ${server} -enroll ${token} -enroll-only -creds /etc/endpoint-agent/creds.json`,
    },
  ]
}
