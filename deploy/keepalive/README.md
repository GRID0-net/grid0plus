# deploy/keepalive

One Python file, standard library only, no build step, and nothing for an
operator to remember: it starts at boot, restarts itself if it dies, and
stops the moment anything else on the machine wants the CPU.

| | |
| --- | --- |
| Reimplements | nothing of Nintendo's — this is host ops, not a service |
| Needs a database | no |
| Talks to a console | never |
| Requires root | only at install time, through `scripts/install-systemd.sh` |

## Why it exists

Oracle's Always Free tier reclaims a compute instance that looks unused. The
current rule, from Oracle's own documentation: an instance is idle when, over
a **seven-day** window, the 95th percentile of its **CPU** utilization, its
**network** utilization and — on A1 shapes — its **memory** utilization are
*all* below the threshold. That is 20% today; the notices people still find
say 10%, so aim above both.

It is an **AND**, and that is the whole lever: holding one of the three up is
enough, and CPU is the one that costs no bandwidth and sends no traffic
anywhere. (Upgrading the tenancy to Pay As You Go removes the policy instead,
but that is not available to everyone — it asks for a card and a hold.)

A GRID0+ box is idle in precisely the sense that policy means. The services
come up in milliseconds and then wait for a console that has not booted yet.
No amount of correctness in the services changes that number, so this changes
it deliberately, and says so in the log.

## What it actually does

- Samples `/proc/stat` for the **whole machine**, not its own usage, so work
  the services really do counts toward the target and only the shortfall is
  made up. A console session drives the duty cycle to zero by itself.
- Corrects the spin time each window against the utilization it just measured,
  damped so it settles instead of oscillating. Target 45% by default; the
  console's own CPU graph is the reading that decides, and 45% clears both
  published thresholds with room to spare.
- Runs as the least important unit on the machine: `Nice=19` and `CPUWeight=1`
  in `deploy/systemd/switchnet-keepalive.service`, so anything real preempts
  it even under contention.
- **Does nothing off Oracle Cloud.** The instance metadata endpoint is the
  switch, and it is re-probed rather than decided once, so metadata that is
  slow to answer at boot costs a delay instead of a governor that never
  starts. A non-OCI host logs that and sleeps.
- Never exits for a reason of its own, and never gives up on an error: each
  window is guarded, and a failed `/proc` read is a logged error and a retry.
  systemd restarts it on any exit regardless.
- Warns once an hour if measured utilization stays under the idle line for
  longer than the control loop could need. A keepalive that has quietly
  stopped working is the failure worth catching, and nobody watches a
  five-second window for a week.

## Installed by

`scripts/install-systemd.sh`, which already copies
`deploy/systemd/switchnet-*.service`; the keepalive is enabled alongside the
target so that one install step covers the whole deployment. It is
deliberately **not** in `switchnet.target`: it is not a Nintendo-facing
service, and nothing that restarts the stack should be entangled with it.

## Checking it

```sh
systemctl status switchnet-keepalive            # is it up, since when
journalctl -u switchnet-keepalive -f            # what it is doing
cat /opt/switchnet/data/keepalive/status.txt    # the current reading
sudo -u switchnet /opt/switchnet/deploy/keepalive/keepalive.py --check
```

`--check` reports whether this is an OCI instance, whether a governor is
already running, and the current utilization, without changing anything.

**After a `git pull` that changes `keepalive.py`, restart the unit:**
`systemctl restart switchnet-keepalive`. Python reads the file when the
process starts, and `scripts/update.sh` deliberately does not restart this
unit -- it is not a Go service, nothing rebuilds it, and a restart is only
warranted when the file itself changed.

**Then look at the instance's CPU graph in the cloud console.** That is the
same data the reclaim decision is made from, and it is the only reading worth
trusting; everything above is a convenience for telling you early when the
governor itself is broken.

## Changing or turning it off

Target, via a systemd drop-in — the script reads the environment, so this
does not mean replacing `ExecStart`:

```sh
systemctl edit switchnet-keepalive
# [Service]
# Environment=KEEPALIVE_TARGET=55
systemctl restart switchnet-keepalive
```

Off:

```sh
systemctl disable --now switchnet-keepalive
```

A target at or below the published idle line is legal — a host with a
different policy may have a lower one — but the log warns about it at
startup, because on Oracle's Always Free tier it achieves nothing.

## What it deliberately does not do

- **No network load and no memory padding.** CPU alone is enough (the rule is
  an AND) and traffic is the part that would reach a network someone else
  pays for.
- **No pretending to be a user or a session.** It burns CPU; it does not
  simulate consoles to make the server look busier than it is.
- **Nothing when it is not on a reclaiming host.** See above: a laptop, a
  VPS, or a second machine runs the unit and idles.
