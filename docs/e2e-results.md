# Real Claude Code e2e results

The tests in `e2e/` need a Claude Code login, so CI can't run them. CI runs the hermetic suite, which drives a fake Claude Code screen. This file records the real runs, newest first. Add a section when you run them on a new Claude Code version or change the queue runner, the screen model or `say`.

## 2026-11-27

Claude Code 2.1.332, Haiku sessions with a Sonnet relaunch, tmux 3.6, Go 1.26.0, Linux 7.0. The machine had 14 GB of RAM, with six to eight other Claude Code sessions open and 2.2–3.6 GB free.

| Test | Pane | Result | Wall time per run |
|---|---|---|---|
| `TestSelfControl`, `GANTLINE_E2E_RUNS=3 GANTLINE_E2E_LIVE=1` | 56x16 | 3 of 3 passed | 1m56s, 2m30s, 2m29s |
| `TestScenarios`, first run | 200x50 | 5 of 6 passed | 40s to 2m52s |
| `TestScenarios`, after the harness fix | 200x50 | 6 of 6 passed | 34s to 1m13s |

The 56x16 pane is short enough that the 900-byte `/compact` scrolls the input box, which is the case the runner most often got wrong in daily use before it learned to read a scrolled box.

Every run's `gantline audit` reported every delivery clean.

The first scenario run's failure was in the test harness. `killed-runner` typed its first prompt into a newly drawn input box that wasn't taking keys yet. `gantline say` reported that none of the text reached the box. The harness now retries a first prompt only on that error.

In 3 of the 15 scenario launches, Claude Code exited during startup. The system log shows no OOM kill. The harness retried each launch once, and each retry passed. The first two exits took the tmux server with them. Test servers now keep a dead pane, and the third exit showed status 1, a blank screen and no transcript. The cause is unknown.
