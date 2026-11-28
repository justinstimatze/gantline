# Real Claude Code e2e results

The tests in `e2e/` need a Claude Code login, so they don't run on every push. CI runs the unit tests on Linux and macOS against a fake Claude Code screen, and the `e2e` workflow runs the real tests on a GitHub runner when started by hand. This file records the real runs, newest first. Add a section when you run them on a new Claude Code version or change the queue runner, the screen model or `say`.

## 2026-11-27, macOS

Claude Code 2.1.332 from npm, Haiku sessions with a Sonnet relaunch, tmux 3.8 from Homebrew, Go 1.26.11, GitHub's `macos-26-arm64` runner, logged in with an API key. Run 46098033324 of the `e2e` workflow:

| Test | Pane | Result | Wall time |
|---|---|---|---|
| `TestSelfControl`, `GANTLINE_E2E_LIVE=1` | 56x16 | 1 of 1 passed | 1m19s |
| `TestScenarios` | 200x50 | 6 of 6 passed | 22s to 56s |

Every delivery's `gantline audit` was clean.

Eleven of the twelve earlier runs that day failed. They turned up these problems, all now fixed:

- npm installs Claude Code as `claude.exe`, which `restart` didn't match.
- The trust dialog's fingerprint missed the dialog with the cursor on "Yes", and the harness pressed Enter on "No, exit" when its Down key was dropped.
- Claude Code draws an input box taller than the pane at full height, with no "↑ N more" rule, so the box's top scrolls into tmux's history. Starting the pane at 56x16 or resizing it there made no difference, and setting `TERM` to `screen-256color` didn't either. gantline now reads such a box from its last rows, and for a `/compact` it checks the instructions Claude Code recorded in the transcript. In the passing run the full text matched and that check wasn't needed, so it has run only in unit tests.
- Claude Code drew a `tmux focus-events off` notice under the mode line, which gantline took for the mode line. That also hid "esc to interrupt" from the busy check.
- A runner waiting behind text it couldn't read as its own logged nothing. It now logs what it waits on.

## 2026-11-27, Linux

Claude Code 2.1.332, Haiku sessions with a Sonnet relaunch, tmux 3.6, Go 1.26.0, Linux 7.0. The machine had 14 GB of RAM, with six to eight other Claude Code sessions open and 2.2–3.6 GB free.

| Test | Pane | Result | Wall time per run |
|---|---|---|---|
| `TestSelfControl`, `GANTLINE_E2E_RUNS=3 GANTLINE_E2E_LIVE=1` | 56x16 | 3 of 3 passed | 1m56s, 2m30s, 2m29s |
| `TestScenarios`, first run | 200x50 | 5 of 6 passed | 40s to 2m52s |
| `TestScenarios`, after the harness fix | 200x50 | 6 of 6 passed | 34s to 1m13s |
| `restart` and `killed-runner`, after the gopsutil port | 200x50 | 2 of 2 passed | 1m23s, 51s |

The 56x16 pane is short enough that the 900-byte `/compact` scrolls the input box, which is the case the runner most often got wrong in daily use before it learned to read a scrolled box.

Every run's `gantline audit` reported every delivery clean.

The first scenario run's failure was in the test harness. `killed-runner` typed its first prompt into a newly drawn input box that wasn't taking keys yet. `gantline say` reported that none of the text reached the box. The harness now retries a first prompt only on that error.

In 3 of the 15 scenario launches, Claude Code exited during startup. The system log shows no OOM kill. The harness retried each launch once, and each retry passed. The first two exits took the tmux server with them. Test servers now keep a dead pane, and the third exit showed status 1, a blank screen and no transcript. The cause is unknown.

The port run hit one more startup exit, again status 1 with a blank screen.

The harness answered the trust dialog with Down then Enter in these runs. On macOS that later picked "No, exit" when the Down was dropped, which also exits with status 1, so it may account for these exits. That hasn't been checked.
