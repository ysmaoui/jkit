# AI agent integration

Drop-in artifacts that let an AI coding agent drive `jk` competently — a
skill, a slash command, and a sub-agent. Authored for
[Claude Code](https://docs.claude.com/en/docs/claude-code/overview); the
prompts and workflows transfer cleanly to other agent runtimes that accept
free-form system prompts.

## What's here

| Path | Type | Purpose |
|---|---|---|
| [`skills/jenkins/SKILL.md`](skills/jenkins/SKILL.md) | Skill | Reference for the full `jk` command surface — auto-loaded when the agent reasons about Jenkins. |
| [`commands/jenkins-monitor.md`](commands/jenkins-monitor.md) | Slash command | `/jenkins-monitor URL` — wait for a build, then summarize the result (with `jk diagnose` on failure). |
| [`subagents/jenkins-build-analyzer.md`](subagents/jenkins-build-analyzer.md) | Sub-agent | Specialized failure-triage agent. Delegated to from the main thread when the user asks "why did the build fail?". |

All three assume `jk` is on `$PATH` and `jk auth login` has been run.

## Install (Claude Code)

User-wide (recommended):

```bash
git clone https://github.com/ysmaoui/jk
cp -r jk/agents/skills/jenkins                ~/.claude/skills/
cp    jk/agents/commands/jenkins-monitor.md   ~/.claude/commands/
cp    jk/agents/subagents/jenkins-build-analyzer.md ~/.claude/agents/
```

Per-project (commit alongside your repo):

```bash
mkdir -p .claude/{skills,commands,agents}
cp -r jk/agents/skills/jenkins                .claude/skills/
cp    jk/agents/commands/jenkins-monitor.md   .claude/commands/
cp    jk/agents/subagents/jenkins-build-analyzer.md .claude/agents/
```

Restart Claude Code (or `/reload`) to pick them up.

## Use with other agent runtimes

The `SKILL.md` body is plain Markdown and works as a system prompt or a
retrieved context chunk for any agent (Cursor, Aider, custom OpenAI/Claude
SDK loops, etc.). Strip the YAML frontmatter and inline the body where your
runtime expects guidance.

The `jenkins-build-analyzer` prompt is similarly portable — feed it as a
sub-task system prompt with a `Bash`-equivalent tool.

## Usage examples

```
> /jenkins-monitor https://jenkins.example.com/job/team/job/svc/42/

> Why did the last build of my-service fail?
  (main thread delegates to jenkins-build-analyzer)

> Trigger my-service with FOO=bar and tail the log
  (skill kicks in, agent runs `jk run my-service -p FOO=bar --wait --log`)
```

## Customizing

- **Restrict hosts** — change `allowed-tools: Bash(command:jk*)` in `SKILL.md`
  to e.g. `Bash(command:jk status*, command:jk log*)` for a read-only profile.
- **Swap the model** — `jenkins-build-analyzer` uses `haiku` for cost; bump to
  a larger model for trickier failure analysis.
- **Add your own** — see the [Claude Code skill docs](https://docs.claude.com/en/docs/claude-code/skills)
  and the existing files as templates.

## Contributing

PRs welcome. Keep skills CLI-flag-accurate against the current `jk` release —
if a flag changes, update `SKILL.md` in the same PR.
