export type ShellResult = {
  stdout: string
  stderr: string
  exitCode: number
  signal?: number
}

export type ShellOptions = {
  cwd?: string
  env?: Record<string, string>
  stdin?: string | Uint8Array | ArrayBuffer | null
}

export const $ = Bun.$

// Run a shell command (or pipeline) and capture stdout/stderr/exit code.
// If command is an array, it is joined with " | " and run with pipefail enabled.
export async function sh(command: string | string[], options: ShellOptions = {}): Promise<ShellResult> {
  const cmd = Array.isArray(command) ? command.join(" | ") : command
  const full = Array.isArray(command) ? `set -o pipefail; ${cmd}` : cmd
  if (!full.trim()) {
    throw new Error("command is required")
  }

  const proc = Bun.spawn({
    cmd: ["/bin/sh", "-lc", full],
    cwd: options.cwd,
    env: options.env,
    stdin: "pipe",
    stdout: "pipe",
    stderr: "pipe",
  })

  if (options.stdin !== undefined && options.stdin !== null) {
    const input =
      typeof options.stdin === "string"
        ? options.stdin
        : options.stdin instanceof ArrayBuffer
          ? new Uint8Array(options.stdin)
          : options.stdin
    proc.stdin.write(input)
  }
  proc.stdin.end()

  const stdout = await new Response(proc.stdout).text()
  const stderr = await new Response(proc.stderr).text()
  const exitCode = await proc.exited

  return { stdout, stderr, exitCode }
}
