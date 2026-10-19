# keepalive

A small helper for servers on cloud hosts that reclaim idle machines. It is one Python file, standard library only, with no build step.
It starts at boot, restarts itself if it dies, and stops using the CPU the moment anything else wants it.

It isn't part of GRID0+'s protocol work and it never talks to a console. Installing it needs root only through
`scripts/install-systemd.sh`.

## Why it exists

Oracle's Always Free tier reclaims a compute instance that looks unused. By Oracle's documentation an instance is idle when, over a
seven-day window, the 95th percentile of its CPU utilization, its network utilization and (on A1 shapes) its memory utilization are all below
the threshold. That is 20% today, though older notices say 10%, so aim above both. It is an AND, so holding one of the three up is enough, and CPU
is the one that costs no bandwidth.

A GRID0+ server is idle in exactly that sense: the services start in milliseconds and then wait for a console that hasn't booted. This helper
changes that number on purpose, and says so in the log.

## What it does

- It samples `/proc/stat` for the whole machine, not its own usage, so real work the services do counts toward the target and only the shortfall
  is made up. A console session drives the duty cycle to zero by itself.
- It corrects its spin time each window against the utilization it just measured, damped so it settles instead of oscillating. The target is 45% by
  default, which clears both published thresholds. The instance's own CPU graph in the cloud console is the reading that decides.
- It runs as the least important unit on the machine (`Nice=19` and `CPUWeight=1` in `deploy/systemd/switchnet-keepalive.service`), so anything real
  preempts it.
- **It does nothing off Oracle Cloud.** The instance metadata endpoint is the switch. It is re-probed instead of decided once, so slow metadata at boot
  costs a delay and not a governor that never starts. A non-Oracle host logs that and sleeps.
- It never exits for a reason of its own. Each window is guarded, and a failed `/proc` read is logged and retried. systemd restarts it on any exit.
- It warns once an hour if measured utilization stays under the idle line for longer than the control loop should need. A keepalive that has quietly
  stopped working is the failure worth catching.

## Installing and checking it

`scripts/install-systemd.sh` installs and enables it beside the rest of the deployment. It isn't in `switchnet.target`, so restarting the stack doesn't
touch it.

```sh
systemctl status switchnet-keepalive            # is it up, since when
journalctl -u switchnet-keepalive -f            # what it is doing
cat /opt/switchnet/data/keepalive/status.txt    # the current reading
sudo -u switchnet /opt/switchnet/deploy/keepalive/keepalive.py --check
```

`--check` reports whether this is an Oracle instance, whether a governor is already running and the current utilization, without changing anything.

After a `git pull` that changes `keepalive.py`, run `systemctl restart switchnet-keepalive`. Python reads the file when the process starts, and
`scripts/update.sh` doesn't restart this unit because it isn't a Go service. Then look at the instance's CPU graph in the cloud console, which is the same
data the reclaim decision uses.

## Changing the target or turning it off

The script reads its target from the environment, so use a systemd drop-in:

```sh
systemctl edit switchnet-keepalive
# [Service]
# Environment=KEEPALIVE_TARGET=55
systemctl restart switchnet-keepalive
```

To turn it off:

```sh
systemctl disable --now switchnet-keepalive
```

A target at or below the published idle line is allowed, since a host with a different policy may have a lower one, but the log warns about it at
start-up because on Oracle's Always Free tier it achieves nothing.

## What it deliberately doesn't do

- **No network load and no memory padding.** CPU alone is enough, and traffic would reach a network someone else pays for.
- **No pretending to be a user.** It burns CPU. It doesn't simulate consoles to make the server look busier than it is.
- **Nothing on a host that isn't reclaiming.** A laptop, a VPS or a second machine runs the unit and idles.
