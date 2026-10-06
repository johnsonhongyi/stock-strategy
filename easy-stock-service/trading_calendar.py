"""Shared trading calendar for A-shares, US equities, and 24/7 crypto."""
import argparse
import datetime
import json
import os
import sys
import tempfile
import threading

SVC = os.path.dirname(os.path.abspath(__file__))
BJ = datetime.timezone(datetime.timedelta(hours=8))
UTC = datetime.timezone.utc
try:
    from zoneinfo import ZoneInfo
    ET = ZoneInfo("America/New_York")
except Exception:
    ET = datetime.timezone(datetime.timedelta(hours=-5))

_DEFAULT_CALENDAR = {
    "schema_version": 1,
    "markets": {
        "CN": {"closed_ranges": [
            {"start": "2026-01-01", "end": "2026-01-03"},
            {"start": "2026-02-15", "end": "2026-02-23"},
            {"start": "2026-04-04", "end": "2026-04-06"},
            {"start": "2026-05-01", "end": "2026-05-05"},
            {"start": "2026-06-19", "end": "2026-06-21"},
            {"start": "2026-09-25", "end": "2026-09-27"},
            {"start": "2026-10-01", "end": "2026-10-07"}],
            "manual_closed_dates": [], "manual_open_dates": []},
        "US": {"manual_closed_dates": [], "manual_open_dates": []},
        "CRYPTO": {"manual_closed_dates": [], "manual_open_dates": []},
    },
}
_calendar_lock = threading.RLock()
_calendar_cache = {"path": None, "mtime_ns": None, "size": None, "data": None}


def _calendar_path():
    configured = os.environ.get("TRADING_CALENDAR_PATH", os.path.join(SVC, "trading-calendar.json"))
    return os.path.realpath(os.path.abspath(configured))


def _load_calendar():
    path = _calendar_path()
    try:
        stat = os.stat(path)
        mtime_ns = getattr(stat, "st_mtime_ns", int(stat.st_mtime * 1e9))
        size = stat.st_size
    except OSError:
        return _DEFAULT_CALENDAR
    with _calendar_lock:
        if (_calendar_cache["path"] == path
                and _calendar_cache["mtime_ns"] == mtime_ns
                and _calendar_cache["size"] == size):
            return _calendar_cache["data"]
        with open(path, "r", encoding="utf-8") as handle:
            data = json.load(handle)
        if data.get("schema_version") != 1 or not isinstance(data.get("markets"), dict):
            raise ValueError("unsupported trading calendar schema: %s" % path)
        _calendar_cache.update(path=path, mtime_ns=mtime_ns, size=size, data=data)
        return data


def _write_calendar(data):
    path = _calendar_path()
    directory = os.path.dirname(path)
    os.makedirs(directory, exist_ok=True)
    data["updated_at"] = datetime.datetime.now(UTC).isoformat(timespec="seconds")
    fd, temp_path = tempfile.mkstemp(prefix=".trading-calendar-", suffix=".tmp", dir=directory)
    try:
        with os.fdopen(fd, "w", encoding="utf-8", newline="\n") as handle:
            json.dump(data, handle, ensure_ascii=False, indent=2)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temp_path, path)
    finally:
        if os.path.exists(temp_path):
            os.remove(temp_path)
    with _calendar_lock:
        _calendar_cache.update(path=None, mtime_ns=None, size=None, data=None)


def _weekday_of_month(year, month, weekday, occurrence):
    first = datetime.date(year, month, 1)
    offset = (weekday - first.weekday()) % 7
    return first + datetime.timedelta(days=offset + 7 * (occurrence - 1))


def _last_weekday_of_month(year, month, weekday):
    if month == 12:
        last = datetime.date(year + 1, 1, 1) - datetime.timedelta(days=1)
    else:
        last = datetime.date(year, month + 1, 1) - datetime.timedelta(days=1)
    return last - datetime.timedelta(days=(last.weekday() - weekday) % 7)


def _easter_sunday(year):
    """Gregorian Easter date (Meeus/Jones/Butcher), used for NYSE Good Friday."""
    a = year % 19
    b, c = divmod(year, 100)
    d, e = divmod(b, 4)
    f = (b + 8) // 25
    g = (b - f + 1) // 3
    h = (19 * a + b - d - g + 15) % 30
    i, k = divmod(c, 4)
    ell = (32 + 2 * e + 2 * i - h - k) % 7
    m = (a + 11 * h + 22 * ell) // 451
    month, day = divmod(h + ell - 7 * m + 114, 31)
    return datetime.date(year, month, day + 1)


def _observed_fixed_date(day):
    if day.weekday() == 5:
        return day - datetime.timedelta(days=1)
    if day.weekday() == 6:
        return day + datetime.timedelta(days=1)
    return day


def _us_exchange_holidays(year):
    """Recurring full-day NYSE closures; early-close sessions remain trading days."""
    holidays = set()
    for holiday_year in (year - 1, year, year + 1):
        new_year = datetime.date(holiday_year, 1, 1)
        # NYSE explicitly does not observe New Year's Day when Jan 1 is Saturday.
        if new_year.weekday() == 6:
            holidays.add(new_year + datetime.timedelta(days=1))
        elif new_year.weekday() < 5:
            holidays.add(new_year)
        if holiday_year >= 1998:
            holidays.add(_weekday_of_month(holiday_year, 1, 0, 3))  # MLK Day
        holidays.add(_weekday_of_month(holiday_year, 2, 0, 3))  # Washington's Birthday
        holidays.add(_easter_sunday(holiday_year) - datetime.timedelta(days=2))  # Good Friday
        holidays.add(_last_weekday_of_month(holiday_year, 5, 0))  # Memorial Day
        if holiday_year >= 2022:
            holidays.add(_observed_fixed_date(datetime.date(holiday_year, 6, 19)))
        holidays.add(_observed_fixed_date(datetime.date(holiday_year, 7, 4)))
        holidays.add(_weekday_of_month(holiday_year, 9, 0, 1))  # Labor Day
        holidays.add(_weekday_of_month(holiday_year, 11, 3, 4))  # Thanksgiving
        holidays.add(_observed_fixed_date(datetime.date(holiday_year, 12, 25)))
    return holidays


def _market_overrides(market):
    return _load_calendar().get("markets", {}).get(market, {})


def is_trading_day(date_s=None, market="CN"):
    """Whether a date is an exchange session; US holidays are generated by rule each year."""
    market = str(market or "CN").strip().upper()
    if market == "CRYPTO":
        return True
    if market != "US":
        market = "CN"
    if date_s is None:
        date_s = today_str(market)
    day = datetime.date.fromisoformat(str(date_s))
    date_key = day.isoformat()
    overrides = _market_overrides(market)
    if date_key in set(overrides.get("manual_open_dates", [])):
        return True
    if date_key in set(overrides.get("manual_closed_dates", [])):
        return False
    if day.weekday() >= 5:
        return False
    if market == "US":
        return day not in _us_exchange_holidays(day.year)
    for closed in overrides.get("closed_ranges", []):
        start = closed.get("start") if isinstance(closed, dict) else closed[0]
        end = closed.get("end") if isinstance(closed, dict) else closed[1]
        if start <= date_key <= end:
            return False
    return True


def today_str(market="CN"):
    """Market-local date: Beijing for CN, New York for US, UTC for crypto."""
    market = str(market or "CN").strip().upper()
    if market == "US":
        return datetime.datetime.now(ET).strftime("%Y-%m-%d")
    if market == "CRYPTO":
        return datetime.datetime.now(UTC).strftime("%Y-%m-%d")
    return datetime.datetime.now(BJ).strftime("%Y-%m-%d")


def _offset_trading_day(date_s, n, market, step):
    if n <= 0:
        raise ValueError("n must be positive")
    day = datetime.date.fromisoformat(date_s)
    found = 0
    for _ in range(365 * 50):
        day += datetime.timedelta(days=step)
        if is_trading_day(day.isoformat(), market):
            found += 1
            if found >= n:
                return day.isoformat()
    raise ValueError("could not find a trading day for %s" % market)


def prev_trading_day(date_s=None, n=1, market="CN"):
    """Find the prior nth session, excluding date_s itself."""
    return _offset_trading_day(date_s or today_str(market), n, market, -1)


def next_trading_day(date_s=None, n=1, market="CN"):
    return _offset_trading_day(date_s or today_str(market), n, market, 1)


def guard_trading_day(script_name="", market="CN"):
    """Non-session days exit quietly, so scheduled strategy jobs do not emit signals."""
    if not is_trading_day(market=market):
        print("非交易日(%s %s),%s 跳过" % (market, today_str(market), script_name or "script"))
        sys.exit(0)


def us_regular_session_hkt(date_s=None):
    """US regular session in Hong Kong time; handles daylight saving via ZoneInfo."""
    date_s = date_s or today_str("US")
    day = datetime.date.fromisoformat(date_s)
    opened = datetime.datetime(day.year, day.month, day.day, 9, 30, tzinfo=ET)
    closed = datetime.datetime(day.year, day.month, day.day, 16, 0, tzinfo=ET)
    return opened.astimezone(BJ), closed.astimezone(BJ)


def us_market_closed(date_s=None):
    """Whether the NYSE core session has reached its regular 4:00 p.m. ET close."""
    _, close_hkt = us_regular_session_hkt(date_s)
    return datetime.datetime.now(BJ) >= close_hkt


def _calendar_cli():
    parser = argparse.ArgumentParser(description="Inspect or override the shared market calendar")
    sub = parser.add_subparsers(dest="command", required=True)
    show = sub.add_parser("show", help="show closed days for a year")
    show.add_argument("market", choices=("CN", "US", "CRYPTO"))
    show.add_argument("year", type=int)
    override = sub.add_parser("override", help="set a manual open/closed date or clear an override")
    override.add_argument("market", choices=("CN", "US", "CRYPTO"))
    override.add_argument("date", help="YYYY-MM-DD")
    override.add_argument("status", choices=("open", "closed", "default"))
    args = parser.parse_args()

    if args.command == "show":
        day = datetime.date(args.year, 1, 1)
        end = datetime.date(args.year + 1, 1, 1)
        while day < end:
            if not is_trading_day(day.isoformat(), args.market):
                print(day.isoformat())
            day += datetime.timedelta(days=1)
        return

    datetime.date.fromisoformat(args.date)
    with _calendar_lock:
        data = json.loads(json.dumps(_load_calendar()))
        market = data.setdefault("markets", {}).setdefault(args.market, {})
        closed = set(market.get("manual_closed_dates", []))
        opened = set(market.get("manual_open_dates", []))
        closed.discard(args.date)
        opened.discard(args.date)
        if args.status == "closed":
            closed.add(args.date)
        elif args.status == "open":
            opened.add(args.date)
        market["manual_closed_dates"] = sorted(closed)
        market["manual_open_dates"] = sorted(opened)
        _write_calendar(data)
    print("Updated %s: %s -> %s (%s)" % (args.market, args.date, args.status, _calendar_path()))


if __name__ == "__main__":
    _calendar_cli()
