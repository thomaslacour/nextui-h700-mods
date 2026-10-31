#!/usr/bin/env python3
"""port-hm.py — bridge between the `port` command and PortMaster's harbourmaster.

The `port` command (Go, paks/updates/cli, same binary as `pak`) never touches
PortMaster's sources or install logic itself: it calls this bridge, which
loads the harbourmaster library shipped in PORTS.pak (with its H700 patches)
and does exactly what the PortMaster GUI does. Output: one JSON document on
stdout; everything harbourmaster prints goes to stderr.

  port-hm.py state [--catalogue] [--check]   installed ports, runtimes, catalogue
  port-hm.py update                          refresh every port source
  port-hm.py install <zip> [--backup DIR]    download, verify (md5), install
  port-hm.py runtime <name.squashfs>         download a runtime if missing

Environment (set by `port`): HM_TOOLS_DIR, HM_PORTS_DIR, HM_SCRIPTS_DIR,
PORT_EMU_DIR (PortMaster folder), PORT_HM_LOG (log file).
"""
import builtins
import json
import os
import shutil
import sys
import traceback
import zlib
from pathlib import Path

# Keep stdout for our JSON only: harbourmaster prints with print()/cprint().
_OUT = os.fdopen(os.dup(1), 'w')
os.dup2(2, 1)
sys.stdout = sys.stderr

EMU_DIR = Path(os.environ.get('PORT_EMU_DIR', Path(os.environ.get('HM_TOOLS_DIR', '.')) / 'PortMaster'))


def reply(doc, code=0):
    _OUT.write(json.dumps(doc, default=str))
    _OUT.write('\n')
    _OUT.flush()
    sys.exit(code)


def load_library():
    pylibs = EMU_DIR / 'pylibs'
    if (EMU_DIR / 'pylibs.zip').is_file():
        reply({'ok': False, 'error': 'PortMaster was updated and is not prepared yet (pylibs.zip): start any port once'}, 1)
    if not (pylibs / 'harbourmaster').is_dir():
        reply({'ok': False, 'error': f'harbourmaster library missing: {pylibs}'}, 1)
    sys.path.insert(0, str(EMU_DIR / 'exlibs'))
    sys.path.insert(0, str(pylibs))
    builtins.PYLIB_PATH = pylibs
    builtins.PORTMASTER_DEBUG = False
    from loguru import logger
    logger.remove()
    log = os.environ.get('PORT_HM_LOG')
    if log:
        try:
            logger.add(log, level='INFO', rotation='1 MB', retention=1)
        except Exception:
            pass
    import harbourmaster
    return harbourmaster, logger


hmlib, logger = load_library()


def _err_write(text, flush=False):
    """Write to stderr; a broken pipe (the ssh link dropped mid-download) must
    not fail the install, so the error is swallowed and the work continues."""
    try:
        sys.stderr.write(text)
        if flush:
            sys.stderr.flush()
    except (BrokenPipeError, OSError):
        pass


class Callback(hmlib.Callback):
    """Download progress and messages on stderr, like the harbourmaster CLI."""

    def __init__(self):
        super().__init__()
        self.tty = sys.stderr.isatty()
        self.last = None
        self.open_line = False

    def progress(self, message, amount, total=None, fmt=None):
        if message is None or fmt != 'data':
            if self.open_line:
                _err_write('\n')
                self.open_line = False
            self.last = None
            return
        if message != self.last:
            if self.open_line:
                _err_write('\n')
                self.open_line = False
            _err_write(f'  {message}\n')
            self.last = message
        if self.tty:
            mb = amount / 1048576
            tot = f' / {total / 1048576:.1f}' if total else ''
            _err_write(f'\r  {mb:.1f}{tot} MB', flush=True)
            self.open_line = True

    def message(self, message):
        if message and not str(message).startswith('- ') and message != 'Loading...':
            _err_write(f'  {message}\n')

    def message_box(self, message):
        if message:
            _err_write(f'  {message}\n')


def open_hm(temp_dir, check):
    return hmlib.HarbourMaster({'no-check': not check, 'quiet': True}, temp_dir=temp_dir, callback=Callback())


def as_list(v):
    if v is None:
        return []
    if isinstance(v, str):
        return [v]
    return list(v)


def source_of(hm, name):
    """First source (priority order) that publishes this port."""
    for prefix, source in hm.sources.items():
        if source.clean_name(name) in source.ports:
            return prefix, source
    return None, None


def src_fields(hm, name, info):
    prefix, _ = source_of(hm, name)
    src = (info or {}).get('source') or {}
    if prefix is None or not src:
        return None
    return {'prefix': prefix, 'md5': src.get('md5'), 'date_added': src.get('date_added'),
            'date_updated': src.get('date_updated'), 'size': src.get('size') or 0,
            'url': src.get('url'), 'downloads': src.get('downloads')}


def owners(hm):
    """item -> number of installed ports that list it (shared items are kept)."""
    count = {}
    for group in (hm.installed_ports, hm.broken_ports):
        for info in group.values():
            for key, val in (info.get('files') or {}).items():
                if key == 'port.json':
                    continue
                for item in as_list(val):
                    count[item] = count.get(item, 0) + 1
    return count


def installed_entry(hm, name, info, broken, count):
    attr = info.get('attr') or {}
    merged = hm.port_info(name, installed=False) or info
    attrs = hm.port_info_attrs(merged)
    items = []
    for key, val in (info.get('files') or {}).items():
        if key != 'port.json':
            items.extend(as_list(val))
    return {
        'name': info.get('name') or name,
        'title': attr.get('title') or name,
        'items': as_list(info.get('items')),
        'files': items,
        'exclusive': [i for i in items if count.get(i, 0) <= 1],
        'md5': (info.get('status') or {}).get('md5'),
        'from': (info.get('status') or {}).get('source'),
        'runtimes': as_list(attr.get('runtime')),
        'broken': broken,
        'update': 'update available' in attrs,
        'src': src_fields(hm, name, merged),
    }


def catalogue_entry(hm, name, info, compatible):
    attr = info.get('attr') or {}
    src = src_fields(hm, name, info) or {}
    return {
        'name': info.get('name') or name, 'title': attr.get('title') or name,
        'desc': attr.get('desc') or '', 'inst': attr.get('inst') or '',
        'genres': as_list(attr.get('genres')), 'porters': as_list(attr.get('porter')),
        'runtimes': as_list(attr.get('runtime')), 'arch': as_list(attr.get('arch')),
        'rtr': bool(attr.get('rtr')), 'exp': bool(attr.get('exp')),
        'availability': attr.get('availability') or '', 'items': as_list(info.get('items')),
        'compatible': compatible, **src,
    }


def cmd_state(hm, args):
    count = owners(hm)
    doc = {'ok': True,
           'device': {'name': hm.device.get('name'), 'device': hm.device.get('device'), 'platform': hm.platform_name},
           'patched': (EMU_DIR / 'pylibs' / '.gt-patched').is_file(),
           'sources': [{'prefix': p, 'name': getattr(s, 'name', p), 'ports': len(s.ports)} for p, s in hm.sources.items()],
           'installed': [installed_entry(hm, n, i, False, count) for n, i in hm.installed_ports.items()]
           + [installed_entry(hm, n, i, True, count) for n, i in hm.broken_ports.items()],
           'unknown': [str(x) for x in hm.unknown_ports],
           'runtimes': []}
    try:
        for rname, data in hm.list_runtimes():
            doc['runtimes'].append({'name': rname, 'status': (data.get('local') or {}).get('status', '')})
    except Exception as err:  # an odd runtimes.json must not hide the ports
        doc['runtimes_error'] = str(err)
    if '--catalogue' in args:
        seen, cat = set(), []
        for prefix, source in hm.sources.items():
            for pname in source.ports:
                key = hmlib.name_cleaner(pname)
                if key in seen:
                    continue
                seen.add(key)
                info = hm.port_info(pname, installed=False)
                if info is None:
                    continue
                cat.append(catalogue_entry(hm, pname, info, bool(hm.match_requirements(info))))
        doc['catalogue'] = cat
    reply(doc)


def cmd_update(hm, args):
    failed = []
    for prefix, source in hm.sources.items():
        try:
            source.update()
        except Exception as err:
            failed.append(f'{prefix}: {err}')
    reply({'ok': not failed, 'error': '; '.join(failed), 'sources': len(hm.sources)}, 1 if failed else 0)


def backup_overwritten(zip_file, ports_dir, backup):
    """Copy aside every existing file the archive would replace with other content."""
    import zipfile
    n = 0
    with zipfile.ZipFile(zip_file) as zf:
        for zi in zf.infolist():
            if zi.is_dir() or zi.filename.startswith('/') or '..' in zi.filename.split('/'):
                continue
            dest = ports_dir / zi.filename
            if not dest.is_file():
                continue
            crc = 0
            with dest.open('rb') as fh:
                for chunk in iter(lambda: fh.read(1 << 20), b''):
                    crc = zlib.crc32(chunk, crc)
            if crc == zi.CRC and dest.stat().st_size == zi.file_size:
                continue
            to = backup / zi.filename
            to.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(dest, to)
            n += 1
    return n


def cmd_install(hm, args):
    if not args:
        reply({'ok': False, 'error': 'install: which port?'}, 2)
    name = args[0]
    backup = None
    if '--backup' in args:
        backup = Path(args[args.index('--backup') + 1])
    prefix, source = source_of(hm, name)
    if source is None:
        reply({'ok': False, 'error': f'{name} is in no PortMaster source'}, 1)
    info = source.download(source.clean_name(name))
    if info is None:
        reply({'ok': False, 'error': f'download of {name} failed (network, or md5 mismatch)'}, 1)
    saved = 0
    if backup is not None:
        saved = backup_overwritten(info['zip_file'], hm.ports_dir, backup)
    rc = hm._install_port(info, do_delete=True)
    if rc != 0:
        reply({'ok': False, 'error': f'harbourmaster could not install {name} (logs/port-harbourmaster.txt)', 'backed_up': saved}, 1)
    hm.load_ports()
    inst = hm.installed_ports.get(hmlib.name_cleaner(name), {})
    missing = [r for r in as_list((inst.get('attr') or {}).get('runtime'))
               if not (hm.libs_dir / (r if r.endswith('.squashfs') else r + '.squashfs')).is_file()]
    src = (info.get('source') or {}) if isinstance(info, dict) else {}
    reply({'ok': True, 'prefix': prefix, 'backed_up': saved,
           'md5': (inst.get('status') or {}).get('md5'), 'date': src.get('date_updated'),
           'missing_runtimes': missing})


def cmd_runtime(hm, args):
    if not args:
        reply({'ok': False, 'error': 'runtime: which runtime?'}, 2)
    rc = hm.check_runtime(args[0])
    reply({'ok': rc == 0, 'error': '' if rc == 0 else f'runtime {args[0]} failed to install'}, 0 if rc == 0 else 1)


COMMANDS = {'state': cmd_state, 'update': cmd_update, 'install': cmd_install, 'runtime': cmd_runtime}


def main(argv):
    if not argv or argv[0] not in COMMANDS:
        reply({'ok': False, 'error': 'usage: port-hm.py state|update|install|runtime'}, 2)
    cmd, args = argv[0], argv[1:]
    check = cmd in ('update', 'install', 'runtime') or '--check' in args
    with hmlib.make_temp_directory() as temp_dir:
        hm = open_hm(temp_dir, check)
        COMMANDS[cmd](hm, args)


if __name__ == '__main__':
    try:
        main(sys.argv[1:])
    except SystemExit:
        raise
    except BaseException as err:  # never leave `port` without an answer
        try:
            logger.error(traceback.format_exc())
        except Exception:
            pass
        reply({'ok': False, 'error': f'{type(err).__name__}: {err}'}, 1)
