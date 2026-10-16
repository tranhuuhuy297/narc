// narc:generated - managed by `narc install opencode`; `narc uninstall opencode` removes it.
//
// narc (https://github.com/huyth45/narc) stops coding agents from weakening
// verification: skipping tests, deleting assertions, `|| true` in CI,
// lowering coverage, type/lint escapes, and so on.
//
//   - tool.execute.before: every edit is checked by `narc hook opencode pre-tool-use`;
//     narc's verdict {deny, reason, message} makes the plugin throw, so the edit
//     never runs and the model sees the reason as the tool error; the user
//     sees the message as a toast.
//   - session.idle: `narc hook opencode stop` checks the session's changes; on
//     {block, reason} the plugin sends the reason back as a follow-up prompt.
//     narc stops asking after hook.max_blocks attempts and returns {message}
//     for the user instead, shown as a toast.
//   - session.created: `narc hook opencode session-start` records the
//     baseline, so issues that existed before the session are ignored.
//
// If narc cannot run, the agent carries on (fail open).
import { spawn } from "node:child_process"

const NARC = __NARC_COMMAND__
const EDIT_TOOLS = new Set(["edit", "write", "multiedit", "apply_patch", "patch"])
const TIMEOUT_MS = 120_000
// A backstop only: narc counts follow-ups itself and gives up first.
const MAX_FOLLOWUPS = 10

type Verdict = { deny?: boolean; block?: boolean; reason?: string; feedback?: string; message?: string }

function narc(event: string, payload: Record<string, unknown>, cwd: string): Promise<Verdict> {
  return new Promise((resolve) => {
    let out = ""
    let settled = false
    const done = (v: Verdict) => {
      if (!settled) {
        settled = true
        resolve(v ?? {})
      }
    }
    let child: ReturnType<typeof spawn>
    try {
      child = spawn(NARC, ["hook", "opencode", event], { cwd, stdio: ["pipe", "pipe", "ignore"] })
    } catch {
      return done({})
    }
    const timer = setTimeout(() => {
      child.kill()
      done({})
    }, TIMEOUT_MS)
    child.on("error", () => {
      clearTimeout(timer)
      done({})
    })
    child.stdout?.on("data", (chunk) => {
      out += chunk
    })
    child.on("close", () => {
      clearTimeout(timer)
      try {
        done(JSON.parse(out || "{}"))
      } catch {
        done({})
      }
    })
    child.stdin?.on("error", () => {})
    child.stdin?.end(JSON.stringify(payload))
  })
}

export const NarcPlugin = async ({ client, directory, worktree }: any) => {
  const cwd: string = directory || worktree || process.cwd()
  const followups = new Map<string, number>()
  const subagentSessions = new Set<string>()
  const toast = async (message?: string) => {
    if (!message) return
    try {
      await client?.tui?.showToast?.({ body: { title: "narc", message, variant: "warning" } })
      // narc:allow error-swallowed reason="the toast is best effort; there is nowhere else to report"
    } catch {}
  }

  return {
    "tool.execute.before": async (input: any, output: any) => {
      const tool = String(input?.tool ?? "").toLowerCase()
      if (!EDIT_TOOLS.has(tool)) return
      const verdict = await narc("pre-tool-use", { session_id: input?.sessionID, tool, args: output?.args ?? {}, cwd }, cwd)
      if (verdict.deny) {
        await toast(verdict.message)
        throw new Error(verdict.reason || "narc blocked this edit")
      }
    },

    event: async ({ event }: any) => {
      const props = event?.properties ?? {}
      if (event?.type === "session.created") {
        const info = props.info ?? {}
        if (info.parentID) {
          // Subagent sessions report to their parent; only the parent's stop is checked.
          subagentSessions.add(info.id)
          return
        }
        if (info.id) await narc("session-start", { session_id: info.id, cwd }, cwd)
        return
      }
      if (event?.type !== "session.idle") return
      const id: string | undefined = props.sessionID
      if (!id || subagentSessions.has(id)) return
      const sent = followups.get(id) ?? 0
      const verdict = await narc("stop", { session_id: id, cwd, loop_count: sent }, cwd)
      if (verdict.block && verdict.reason && sent < MAX_FOLLOWUPS) {
        followups.set(id, sent + 1)
        const request = { path: { id }, body: { parts: [{ type: "text", text: verdict.reason }] } }
        try {
          const session = client?.session
          if (typeof session?.promptAsync === "function") await session.promptAsync(request)
          else session?.prompt?.(request)?.catch?.(() => {})
          // narc:allow error-swallowed reason="fail open: a lost follow-up must not break the session"
        } catch {}
        return
      }
      followups.delete(id)
      await toast(verdict.message)
    },
  }
}
