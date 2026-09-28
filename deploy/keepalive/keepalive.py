#!/usr/bin/env python3
"""Hold this instance's CPU utilization above the cloud's idle-reclaim line.

Oracle's Always Free tier reclaims a compute instance that looks unused. An
instance is idle when, across a seven-day window, the 95th percentile of its
CPU utilization, its network utilization and -- on A1 shapes -- its memory
utilization are *all* below the threshold: 20% in the documentation as of
2026-09, 10% in the 2023 notices and the emails people still find. It is an
AND, so holding one of the three up is enough, and CPU is the one that costs
no bandwidth and sends no traffic anywhere.

A GRID0+ box is idle in precisely the sense that policy means. Nine Go
services, Postgres, Redis and nginx come up in milliseconds and then wait for
a console that has not booted yet. Nothing about the services can fix that
honestly, so this does it deliberately -- and stops the instant anything else
wants the CPU.

How it behaves, because all of that is the point:

  * It samples /proc/stat for the *whole machine*, not its own usage, so real
    work by the services counts toward the target and only the shortfall is
    made up. A console session therefore drives the duty cycle to zero on
    its own, and a box busy for any other reason is left alone.
  * systemd runs it as the least important unit on the machine (Nice=19,
    CPUWeight=1) and restarts it on any exit, so a crash, a reboot or an
    operator mistake is never a reason for the instance to look idle.
  * Off Oracle Cloud it does nothing at all. The instance metadata endpoint
    is the switch, and it is re-probed rather than decided once, so metadata
    that is slow to answer at boot costs a delay instead of a keepalive that
    never starts.
  * It never exits for a reason of its own and never gives up on an error.
    Each window is guarded; a failed /proc read is a logged error and a
    retry, not a death.
  * If measured utilization stays under the idle line long enough to matter,
    it says so, once an hour. A keepalive that has quietly stopped working is
    the failure mode worth catching, and nobody is watching a five-second
    window for a week.

Read the current state with `--check`, or the status file it keeps (default
data/keepalive/status.txt under this checkout). The instance's own CPU graph
in the cloud console is the reading that actually decides reclamation, and
that is the one to look at when changing any of this.
"""

import argparse
import errno
import fcntl
import json
import logging
import os
import signal
import sys
import time
import urllib.error
import urllib.request

# The Oracle instance metadata service. Link-local, answered only to a
# request carrying the token below, and reachable on an OCI instance from
# anything on the box -- which is what makes it usable as a "am I in the
# cloud that reclaims idle machines" switch.
METADATA_URL = "http://169.254.169.254/opc/v2/instance/"
METADATA_TIMEOUT = 2.0
METADATA_RETRY = 60.0

# The line the policy measures against, used only to decide when the governor
# has to admit it is not achieving anything. The target is configured, and a
# target at or below this is legal -- a host with a different policy may have
# a lower line -- so it warns at startup instead of refusing to run.
IDLE_LINE_PERCENT = 20

# Correction gain for the duty cycle. Below 1 so the loop settles rather than
# oscillates around the target; high enough to recover inside a few windows
# when real work appears or disappears.
DAMPING = 0.7

# How long the measured utilization may sit under the idle line before the
# log says so, and how often it is allowed to repeat that. Ten minutes is
# far longer than the control loop needs to converge, so being under the line
# that long means something real is wrong: the spin is not counting, the
# cgroup is capping it, or the metric is not what this measures.
STALL_SECONDS = 600
STALL_REPEAT = 3600

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
DEFAULT_STATUS_DIR = os.path.join(REPO_ROOT, "data", "keepalive")
FALLBACK_STATUS_DIR = "/tmp/grid0plus-keepalive"

STOP = False


def _stop(*_):
    global STOP
    STOP = True


def parse_args(argv=None):
    p = argparse.ArgumentParser(
        description="Hold this instance's CPU utilization above the cloud's idle-reclaim line.")
    p.add_argument("--target", type=float,
                   default=float(os.environ.get("KEEPALIVE_TARGET", "45")),
                   help="whole-instance CPU utilization to hold, in percent (default 45)")
    p.add_argument("--window", type=float,
                   default=float(os.environ.get("KEEPALIVE_WINDOW", "5")),
                   help="seconds per control window (default 5)")
    p.add_argument("--status-file", default=os.environ.get("KEEPALIVE_STATUS_FILE"),
                   help="where to keep the current reading (default data/keepalive/status.txt "
                        "under this checkout, falling back to %s)" % FALLBACK_STATUS_DIR)
    p.add_argument("--for", dest="for_seconds", type=float, default=0,
                   help="stop after this many seconds instead of running until stopped "
                        "(a smoke test, not a deployment)")
    p.add_argument("--check", action="store_true",
                   help="report whether this is an OCI instance, whether a keepalive is "
                        "already running, and the current CPU utilization, then exit")
    p.add_argument("--force", action="store_true",
                   help="govern even if the instance metadata endpoint does not answer, for a "
                        "host behind something that blocks link-local metadata")
    return p.parse_args(argv)


def clamp(value, low, high):
    return low if value < low else high if value > high else value


def status_dir(args, log):
    """A directory this process can write to, whatever happens to the default."""
    if args.status_file:
        directory = os.path.dirname(os.path.abspath(args.status_file))
    else:
        args.status_file = os.path.join(DEFAULT_STATUS_DIR, "status.txt")
        directory = DEFAULT_STATUS_DIR
    try:
        os.makedirs(directory, exist_ok=True)
        probe = os.path.join(directory, ".keepalive-writable")
        with open(probe, "w"):
            pass
        os.unlink(probe)
        return directory
    except OSError as err:
        log.warning("status directory %s is not writable (%s); using %s instead",
                    directory, err, FALLBACK_STATUS_DIR)
        os.makedirs(FALLBACK_STATUS_DIR, exist_ok=True)
        args.status_file = os.path.join(FALLBACK_STATUS_DIR, "status.txt")
        return FALLBACK_STATUS_DIR


def acquire_lock(directory, log):
    """One governor per machine, whoever starts it.

    A second start -- an old cron entry, a manual run, the unit plus a
    developer poking at it -- must not double the load or fight the control
    loop over the duty cycle. It waits instead of exiting, so nothing has to
    be restarted for the right one to win.
    """
    path = os.path.join(directory, "keepalive.lock")
    announced = False
    while not STOP:
        fd = os.open(path, os.O_RDWR | os.O_CREAT, 0o644)
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            return fd
        except OSError as err:
            if err.errno not in (errno.EACCES, errno.EAGAIN):
                os.close(fd)
                raise
            if not announced:
                log.info("another keepalive holds %s; waiting for it to exit", path)
                announced = True
            os.close(fd)
            for _ in range(int(METADATA_RETRY)):
                if STOP:
                    return None
                time.sleep(1)
    return None


def lock_is_held(directory):
    """For --check: is a governor already running? Does not disturb one that is."""
    path = os.path.join(directory, "keepalive.lock")
    try:
        fd = os.open(path, os.O_RDWR | os.O_CREAT, 0o644)
    except OSError:
        return False
    try:
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        fcntl.flock(fd, fcntl.LOCK_UN)
        return False
    except OSError:
        return True
    finally:
        os.close(fd)


def probe_instance():
    """(region, shape) if this is an Oracle Cloud instance, else (None, None).

    The Authorization header is not a credential anyone can use: it is the
    fixed string Oracle requires on the link-local metadata service, which is
    why the request is safe to make repeatedly and why an answer can be
    trusted as evidence of the platform.
    """
    request = urllib.request.Request(METADATA_URL,
                                     headers={"Authorization": "Bearer Oracle"})
    try:
        with urllib.request.urlopen(request, timeout=METADATA_TIMEOUT) as response:
            body = response.read(1 << 16)
    except (urllib.error.URLError, OSError, ValueError):
        return None, None
    try:
        instance = json.loads(body)
    except ValueError:
        return None, None
    if not isinstance(instance, dict) or "region" not in instance:
        return None, None
    return instance.get("region", "?"), instance.get("shape", "?")


def read_cpu():
    """(total, idle) jiffies for the whole machine. iowait counts as idle,
    which is what the number the cloud console shows does too."""
    with open("/proc/stat", "rb") as fh:
        fields = [int(x) for x in fh.readline().split()[1:9]]
    if len(fields) < 8:
        raise OSError("unexpected /proc/stat layout")
    user, nice, system, idle, iowait, irq, softirq, steal = fields
    return user + nice + system + idle + iowait + irq + softirq + steal, idle + iowait


def busy_between(first, second):
    (total1, idle1), (total2, idle2) = first, second
    total = total2 - total1
    if total <= 0:
        return None
    return clamp(1.0 - (idle2 - idle1) / total, 0.0, 1.0)


def sample(window):
    first = read_cpu()
    end = time.monotonic() + window
    while time.monotonic() < end and not STOP:
        time.sleep(min(0.25, end - time.monotonic()))
    return busy_between(first, read_cpu())


def write_status(path, text, log):
    """Atomic, so a reader (or the operator's cat) never sees half a line."""
    try:
        tmp = path + ".tmp"
        with open(tmp, "w") as fh:
            fh.write(text)
        os.replace(tmp, path)
        return True
    except OSError as err:
        log.warning("could not write %s: %s", path, err)
        return False


def govern(args, log, status_path, instance, cores):
    window = args.window
    target = args.target / 100.0
    # Spin on one core: our share of a whole-machine average is
    # spin/(window*cores), which is what the correction below inverts.
    spin = window * clamp(target * cores, 0.0, 0.97)
    deadline = None if args.for_seconds <= 0 else time.monotonic() + args.for_seconds

    started = time.time()
    logged = 0
    below_since = None
    last_status_write = 0.0
    last_stall_log = 0.0
    region, shape = instance

    log.info("holding CPU at %.0f%% of %d core(s) in %.0fs windows", args.target, cores, window)
    while not STOP and (deadline is None or time.monotonic() < deadline):
        try:
            first = read_cpu()
            start = time.monotonic()
            spin_until = start + spin
            while time.monotonic() < spin_until and not STOP:
                pass
            rest = start + window - time.monotonic()
            if rest > 0 and not STOP:
                for _ in range(int(rest * 4) + 1):
                    if STOP or time.monotonic() >= start + window:
                        break
                    time.sleep(0.25)
            busy = busy_between(first, read_cpu())
            if busy is None:
                time.sleep(1)
                continue

            spin = clamp(spin + DAMPING * (target - busy) * window * cores,
                         0.0, window * 0.97)

            now = time.time()
            # Once a minute is often enough for a human, and cheap: the
            # console's own metric graph is the reading that decides.
            if logged == 0 or now - last_status_write > 60:
                write_status(status_path,
                             "target=%.0f%% busy=%.1f%% spin=%.0f%% cores=%d "
                             "instance=%s/%s since=%s\n" % (
                                 args.target, busy * 100, 100 * spin / window, cores,
                                 region, shape,
                                 time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(started))),
                             log)
                last_status_write = now
            logged += 1

            if logged % 12 == 1:  # roughly once a minute
                log.info("cpu busy=%.1f%% spin=%.0f%% of window", busy * 100, 100 * spin / window)

            if busy * 100 < IDLE_LINE_PERCENT:
                below_since = below_since or now
                if now - below_since > STALL_SECONDS and now - last_stall_log > STALL_REPEAT:
                    log.warning("utilization has been under %d%% for %d minutes while this "
                                "governor was aiming for %.0f%% -- it is not achieving "
                                "anything; check the instance's own CPU graph before "
                                "trusting that it is running",
                                IDLE_LINE_PERCENT, int(now - below_since), args.target)
                    last_stall_log = now
            else:
                below_since = None
        except OSError as err:
            # A /proc read that failed once is not a reason to exit and leave
            # the box looking idle for the rest of the week.
            log.error("cpu sampling failed: %s", err)
            time.sleep(5)
        except Exception:  # noqa: BLE001 - the loop must outlive anything it hits
            log.exception("unexpected error in the control loop; continuing")
            time.sleep(5)
    log.info("stopping")


def main(argv=None):
    args = parse_args(argv)
    log = logging.getLogger("keepalive")
    logging.basicConfig(
        level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s",
        datefmt="%Y-%m-%dT%H:%M:%S")

    if args.target <= IDLE_LINE_PERCENT:
        log.warning("target %.0f%% is at or below the %d%% line Oracle documents for idle "
                    "Always Free instances; it will not prevent reclamation there",
                    args.target, IDLE_LINE_PERCENT)

    directory = status_dir(args, log)
    cores = os.cpu_count() or 1

    if args.check:
        region, shape = probe_instance()
        running = lock_is_held(directory)
        print("instance: %s" % ("%s %s" % (region, shape) if region else "not an Oracle Cloud instance"))
        print("keepalive: %s" % ("already running" if running else "not running"))
        try:
            busy = sample(5.0)
        except OSError as err:
            # /proc is Linux-only, and --check is worth running anywhere.
            busy = None
            print("cpu: unavailable (%s)" % err)
        if busy is not None:
            print("cpu: %.1f%% busy over 5s (target %.0f%%)" % (busy * 100, args.target))
        print("status: %s" % args.status_file)
        return 0

    signal.signal(signal.SIGTERM, _stop)
    signal.signal(signal.SIGINT, _stop)

    lock = acquire_lock(directory, log)
    if lock is None:
        return 0

    while not STOP:
        region, shape = probe_instance()
        if region:
            log.info("Oracle Cloud instance: region=%s shape=%s", region, shape)
            govern(args, log, args.status_file, (region, shape), cores)
            return 0
        if args.force:
            log.warning("not an OCI instance (--force): governing anyway")
            govern(args, log, args.status_file, ("?", "?"), cores)
            return 0
        log.info("no Oracle Cloud instance metadata here, so nothing to hold up; re-checking "
                 "in %d minutes", int(METADATA_RETRY / 60))
        for _ in range(int(METADATA_RETRY)):
            if STOP:
                return 0
            time.sleep(1)
    return 0


if __name__ == "__main__":
    sys.exit(main())
