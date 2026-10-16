package environment

// Only simple apt-get update/install commands are optimized. Arbitrary shell
// setup keeps its original semantics, including version pins and options.
const cloudBuildSetupPython = `def needed_setup(command):
    import shlex
    parts = command.split('&&')
    install = parts[-1].strip()
    try: args = shlex.split(install)
    except ValueError: return command
    if len(args) < 3 or args[:2] != ['apt-get', 'install']: return command
    if len(parts) > 2 or (len(parts) == 2 and parts[0].strip() != 'apt-get update'): return command
    packages = []
    options = []
    for arg in args[2:]:
        if arg in ('-y', '--yes', '--no-install-recommends'): options.append(arg)
        elif re.fullmatch(r'[a-z0-9][a-z0-9+.-]*', arg): packages.append(arg)
        else: return command
    if not packages: return command
    missing = []
    for package in packages:
        status = subprocess.run(['dpkg', '-s', package], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL) if shutil.which('dpkg') else None
        if status is None or status.returncode != 0 or b'Status: install ok installed' not in status.stdout:
            missing.append(package)
    if not missing: return None
    return ('apt-get update && ' if len(parts) == 2 else '') + shlex.join(['apt-get', 'install'] + options + missing)
`
