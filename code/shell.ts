export type ExecResult = {
  stdout: string
  stderr: string
  exitCode: number
  signal?: number
}

export type ExecOptions = {
  cwd?: string
  env?: Record<string, string>
}

// Run a shell command and capture stdout/stderr/exit code.
export async function exec(command: string, options: ExecOptions = {}): Promise<ExecResult> {
  if (!command.trim()) {
    throw new Error("command is required")
  }
  const proc = Bun.spawn({
    cmd: ["/bin/sh", "-lc", command],
    cwd: options.cwd,
    env: options.env,
    stdout: "pipe",
    stderr: "pipe",
  })

  const stdout = await new Response(proc.stdout).text()
  const stderr = await new Response(proc.stderr).text()
  const exitCode = await proc.exited

  return { stdout, stderr, exitCode }
}

// Alias for exec.
export async function run(command: string, options: ExecOptions = {}): Promise<ExecResult> {
  return exec(command, options)
}
