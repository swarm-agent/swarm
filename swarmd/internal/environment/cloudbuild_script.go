package environment

import (
	"encoding/base64"
	"encoding/json"
)

// cloudBuildBootstrap fetches the integrity-bound runner using the same metadata
// token and stdin curl configuration as the payload transfer. Only this small
// bootstrap goes through Cloud Build argument substitution.
func cloudBuildBootstrap(bucket, prefix, sha string) string {
	data, _ := json.Marshal(map[string]string{"bucket": bucket, "prefix": prefix, "sha256": sha})
	return "set -euo pipefail\npython3 - '" + base64.StdEncoding.EncodeToString(data) + "' <<'SWARM_BOOT'\n" + cloudBuildBootstrapPython + "\nSWARM_BOOT\n"
}

const cloudBuildBootstrapPython = `import base64, hashlib, json, os, subprocess, sys, tempfile, urllib.parse, urllib.request
cfg = json.loads(base64.b64decode(sys.argv[1]))
req = urllib.request.Request('http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token', headers={'Metadata-Flavor':'Google'})
with urllib.request.urlopen(req, timeout=30) as r: token = json.load(r)['access_token']
url = 'https://storage.googleapis.com/storage/v1/b/' + urllib.parse.quote(cfg['bucket'], safe='') + '/o/' + urllib.parse.quote(cfg['prefix']+'script.sh', safe='') + '?alt=media'
with tempfile.TemporaryDirectory() as directory:
    path = os.path.join(directory, 'script.sh')
    subprocess.run(['curl','--fail','--silent','--show-error','--max-time','300','--config','-','-o',path,url], input=('header = "Authorization: Bearer '+token+'"\n').encode(), check=True, stdout=subprocess.DEVNULL)
    with open(path, 'rb') as f: digest = hashlib.sha256(f.read()).hexdigest()
    if digest != cfg['sha256']: raise ValueError('script SHA256 mismatch')
    subprocess.run(['bash',path], check=True)
`

// cloudBuildScript requires bash, python3, curl, tar, git and GNU timeout in the
// chosen base image, plus useradd and runuser. Infrastructure errors escape;
// check exit codes are data.
func cloudBuildScript(bucket, prefix, sha string) string {
	return cloudBuildScriptWithCache(bucket, prefix, sha, "")
}

func cloudBuildScriptWithCache(bucket, prefix, sha, key string) string {
	return cloudBuildCheckScript(bucket, prefix, sha, key, prefix, -1, true)
}

func cloudBuildCheckScript(bucket, prefix, sha, key, resultPrefix string, index int, save bool) string {
	data, _ := json.Marshal(map[string]any{"bucket": bucket, "prefix": prefix, "sha256": sha, "cache_key": key, "result_prefix": resultPrefix, "check_index": index, "cache_save": save})
	return "set -euo pipefail\nIMAGE_PATH=\"$PATH\"\nexport IMAGE_PATH\nexport GOPATH=/workspace/go\nexport GOCACHE=/workspace/.cache/go-build\nexport GOMODCACHE=/workspace/go/pkg/mod\npython3 - '" + base64.StdEncoding.EncodeToString(data) + "' <<'SWARM_PY'\n" + cloudBuildPython + "\nSWARM_PY\n"
}

const cloudBuildPython = `import base64, collections, hashlib, json, os, pwd, re, subprocess, sys, time, urllib.parse, urllib.request, shutil, tarfile, uuid, threading
cfg = json.loads(base64.b64decode(sys.argv[1]))
root = '/workspace'
phases = {}
phase_lines = []
def phase(name, start):
    seconds = round(time.monotonic()-start, 3)
    phases[name] = seconds
    line = 'SWARM_PHASE '+name+' '+format(seconds, '.3f')
    phase_lines.append(line)
    print(line, flush=True)
def token():
    req = urllib.request.Request('http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token', headers={'Metadata-Flavor':'Google'})
    with urllib.request.urlopen(req, timeout=30) as r: return json.load(r)['access_token']
def transfer(name, path, upload=False, object_name=None):
    obj = object_name if object_name is not None else (cfg.get('result_prefix', cfg['prefix']) if upload else cfg['prefix']) + name
    if upload:
        url = 'https://storage.googleapis.com/upload/storage/v1/b/' + urllib.parse.quote(cfg['bucket'], safe='') + '/o?uploadType=media&name=' + urllib.parse.quote(obj, safe='')
    else:
        url = 'https://storage.googleapis.com/storage/v1/b/' + urllib.parse.quote(cfg['bucket'], safe='') + '/o/' + urllib.parse.quote(obj, safe='') + '?alt=media'
    # Token is passed through stdin configuration, not argv or output.
    args = ['curl','--fail','--silent','--show-error','--max-time','300','--config','-']
    args += ['-X','POST','--data-binary','@'+path] if upload else ['-o',path]
    if object_name is not None and not upload: args += ['--max-filesize', str(3 * 1024**3)]
    subprocess.run(args + [url], input=('header = "Authorization: Bearer '+token()+'"\n').encode(), check=True, stdout=subprocess.DEVNULL)
def cache_publish(source, destination):
    base = 'https://storage.googleapis.com/storage/v1/b/' + urllib.parse.quote(cfg['bucket'], safe='') + '/o/'
    url = base + urllib.parse.quote(source, safe='') + '/copyTo/b/' + urllib.parse.quote(cfg['bucket'], safe='') + '/o/' + urllib.parse.quote(destination, safe='')
    req = urllib.request.Request(url, data=b'', method='POST', headers={'Authorization':'Bearer '+token()})
    with urllib.request.urlopen(req, timeout=300) as response: response.read()
    req = urllib.request.Request(base+urllib.parse.quote(source, safe=''), method='DELETE', headers={'Authorization':'Bearer '+token()})
    with urllib.request.urlopen(req, timeout=30) as response: response.read()
# Persistent cache helpers
import hashlib, subprocess
cache_before = None
CACHE_LIMIT = 3 * 1024**3
cache_paths = ('.cache/go-build', 'go/pkg/mod', '.cache/pnpm')
def cache_object(): return 'cache/'+cfg['cache_key']+'/latest.tar.gz'
def cold_cache():
    for name in cache_paths:
        path = root+'/'+name
        if os.path.islink(path): os.unlink(path)
        elif os.path.exists(path): shutil.rmtree(path)
        os.makedirs(path, exist_ok=True)
def restore_cache():
    archive_path = root+'/restore-cache.tar.gz'
    unpacked = root+'/restore-cache.tar'
    try:
        transfer('', archive_path, object_name=cache_object())
        if os.path.getsize(archive_path) > CACHE_LIMIT: raise ValueError('cache too large')
        # Preserve the object key and detect compression by magic.
        with open(archive_path, 'rb') as source: zstd = source.read(4) == b'\x28\xb5\x2f\xfd'
        if zstd:
            with open(unpacked, 'wb') as dest:
                proc = subprocess.Popen(['zstd','-d','-c',archive_path], stdout=subprocess.PIPE)
                try:
                    expanded = 0
                    while True:
                        chunk = proc.stdout.read(65536)
                        if not chunk: break
                        expanded += len(chunk)
                        if expanded > CACHE_LIMIT + 512*1024**2: raise ValueError('expanded cache too large')
                        dest.write(chunk)
                    if proc.wait() != 0: raise ValueError('invalid zstd cache')
                finally:
                    if proc.poll() is None: proc.kill()
                    proc.wait()
                    proc.stdout.close()
        with tarfile.open(unpacked if zstd else archive_path, 'r:' if zstd else 'r:gz') as archive:
            size = 0
            members = []
            for member in archive:
                name = member.name.rstrip('/')
                if ('..' in name.split('/') or not any(name == p or name.startswith(p+'/') for p in cache_paths)
                    or not (member.isfile() or member.isdir())):
                    raise ValueError('unsafe cache entry')
                size += member.size
                if size > CACHE_LIMIT or len(members) >= 500000: raise ValueError('expanded cache too large')
                members.append(member)
            cold_cache()
            # Only validated regular files/directories, never links or devices.
            for member in members:
                path = root+'/'+member.name
                if member.isdir(): os.makedirs(path, exist_ok=True)
                else:
                    os.makedirs(os.path.dirname(path), exist_ok=True)
                    with archive.extractfile(member) as source, open(path,'wb') as dest:
                        shutil.copyfileobj(source, dest)
                    os.chmod(path, member.mode & 0o777)
                    os.utime(path, (member.mtime, member.mtime))
        print('Cloud Build cache restored')
    except Exception as err:
        cold_cache()
        print('Cloud Build cache miss or invalid archive; continuing cold: '+str(err))
    finally:
        if os.path.exists(archive_path): os.unlink(archive_path)
        if os.path.exists(unpacked): os.unlink(unpacked)
def cache_manifest():
    # Metadata avoids reading hundreds of MB on the critical path.
    digest = hashlib.sha256()
    for name in cache_paths:
        for directory, dirs, files in os.walk(root+'/'+name, followlinks=False):
            dirs.sort()
            for file in sorted(files):
                path = os.path.join(directory, file)
                info = os.lstat(path)
                digest.update(repr((os.path.relpath(path, root), info.st_mode, info.st_size, info.st_mtime_ns)).encode())
    return digest.hexdigest()
def save_cache():
    archive_path = root+'/save-cache.tar.gz'
    try:
        if cache_before == cache_manifest():
            print('Cloud Build cache unchanged; skipping upload')
            return
        size = 0
        for name in cache_paths:
            for directory, dirs, files in os.walk(root+'/'+name, followlinks=False):
                for file in files:
                    size += os.lstat(os.path.join(directory,file)).st_size
                    if size > CACHE_LIMIT:
                        print('Cloud Build cache exceeds size cap; skipping upload')
                        return
        def add_cache(archive):
            archive.dereference = False
            for name in cache_paths:
                archive.add(root+'/'+name, arcname=name, filter=lambda m: m if m.isfile() or m.isdir() else None)
        if shutil.which('zstd'):
            with open(archive_path, 'wb') as dest:
                proc = subprocess.Popen(['zstd','-T0','-1','-c'], stdin=subprocess.PIPE, stdout=dest)
                try:
                    with tarfile.open(fileobj=proc.stdin, mode='w|') as archive: add_cache(archive)
                    proc.stdin.close()
                    if proc.wait() != 0: raise ValueError('zstd compression failed')
                finally:
                    if proc.poll() is None: proc.kill()
                    proc.wait()
        else:
            with tarfile.open(archive_path, 'w:gz', compresslevel=1) as archive: add_cache(archive)
        if os.path.getsize(archive_path) > CACHE_LIMIT:
            print('Cloud Build cache archive exceeds size cap; skipping upload')
            return
        temporary = 'cache/'+cfg['cache_key']+'/tmp-'+uuid.uuid4().hex+'.tar.gz'
        transfer('', archive_path, True, object_name=temporary)
        cache_publish(temporary, cache_object())
    except Exception as err:
        print('Cloud Build cache save failed; continuing: '+str(err))
    finally:
        if os.path.exists(archive_path): os.unlink(archive_path)
# End persistent cache helpers
cache_errors = []
def restore_phase():
    start = time.monotonic()
    try:
        if cfg.get('cache_key'): restore_cache()
    except Exception as err:
        cache_errors.append(err)
    finally:
        phase('cache_restore', start)
cache_thread = threading.Thread(target=restore_phase)
cache_thread.start()
payload_start = time.monotonic()
transfer('payload.tar.gz', root+'/payload.tar.gz')
transfer('request.json', root+'/request.json')
with open(root+'/payload.tar.gz','rb') as f:
    digest = hashlib.file_digest(f, 'sha256').hexdigest() if hasattr(hashlib,'file_digest') else None
if digest is None:
    h = hashlib.sha256()
    with open(root+'/payload.tar.gz','rb') as f:
        for chunk in iter(lambda:f.read(65536), b''): h.update(chunk)
    digest = h.hexdigest()
if digest != cfg['sha256']: raise RuntimeError('payload sha256 mismatch')
with open(root+'/request.json') as f: request = json.load(f)
if cfg.get('check_index', -1) >= 0: request['checks'] = [request['checks'][cfg['check_index']]]
os.mkdir(root+'/src')
subprocess.run(['tar','-xzf',root+'/payload.tar.gz','-C',root+'/src'],check=True)
os.chdir(root+'/src')
env = os.environ.copy()
env.update(request['definition'].get('Env') or {})
# The image path is captured before any login shell or definition overrides.
env['IMAGE_PATH'] = os.environ['IMAGE_PATH']
cache_env = {'GOPATH': root+'/go', 'GOCACHE': root+'/.cache/go-build', 'GOMODCACHE': root+'/go/pkg/mod',
             'TMPDIR': root+'/tmp', 'XDG_CACHE_HOME': root+'/.cache', 'XDG_DATA_HOME': root+'/.local/share',
             'npm_config_cache': root+'/.cache/npm', 'npm_config_store_dir': root+'/.cache/pnpm',
             'PNPM_HOME': root+'/.local/share/pnpm'}
env.update(cache_env)
phase('payload', payload_start)
cache_thread.join()
if cache_errors: raise cache_errors[0]
for path in cache_env.values(): os.makedirs(path, exist_ok=True)
cache_before = cache_manifest() if cfg.get('cache_key') else None
setup_start = time.monotonic()
def login_command(command):
    return 'export PATH="$IMAGE_PATH:$PATH"; ' + command
` + cloudBuildSetupPython + `
for command in request['definition'].get('Setup') or []:
    command = needed_setup(command)
    if command is None: continue
    subprocess.run(['bash','-lc',login_command(command)],env=env,check=True,timeout=request['timeout'])
phase('setup', setup_start)
git_start = time.monotonic()
git = ['git','-c','core.fsmonitor=false','-c','gc.auto=0']
subprocess.run(git+['init','-q'],check=True)
subprocess.run(git+['add','-A'],check=True)
subprocess.run(git+['-c','user.name=Swarm Landing','-c','user.email=landing@swarm.local','commit','-q','--allow-empty','-m','Landing tree'],check=True)
phase('git', git_start)
# Setup and Git initialization stay root-owned operations. Only the check
# subprocesses drop privileges; results and uploads remain in this root process.
check_home = root+'/home/swarmcheck'
os.makedirs(root+'/home', exist_ok=True)
try:
    check_user = pwd.getpwnam('swarmcheck')
except KeyError:
    subprocess.run(['useradd','-m','-u','10001','-d',check_home,'swarmcheck'],check=True)
    check_user = pwd.getpwnam('swarmcheck')
if check_user.pw_uid == 0: raise RuntimeError('swarmcheck must not be root')
os.makedirs(check_home, exist_ok=True)
subprocess.run(['chown','-R',str(check_user.pw_uid)+':'+str(check_user.pw_gid),
                root+'/src',check_home,*dict.fromkeys(cache_env.values())],check=True)
env.update({'HOME': check_home, 'USER': 'swarmcheck', 'LOGNAME': 'swarmcheck',
            'PATH': env['IMAGE_PATH']+':'+env.get('PATH', '')})
env['SWARM_LANDING_HEAD'] = request['head']
env['SWARM_LANDING_BASE'] = request['base']
checks = []
output = bytearray()
markers = re.compile(rb'FAIL|error:|panic:|fatal:|AssertionError|FAILED|Error:')
# Per-check loop: timeout kills the process group, and output is drained while
# retaining only bounded tails and failure-marker context.
for command in request['checks']:
    start = time.monotonic()
    proc = subprocess.Popen(['timeout','--kill-after=5s',str(request['timeout'])+'s','runuser','-u','swarmcheck','--preserve-environment','--','bash','-c',command],env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT)
    tail = bytearray(); failures = bytearray(); before = collections.deque(maxlen=3); after = 0
    while True:
        line = proc.stdout.readline(4096)
        if not line: break
        tail.extend(line); del tail[:-16384]
        output.extend(line); del output[:-2097152]
        if markers.search(line):
            failures.extend(b''.join(before)); after = 3
        if after:
            failures.extend(line); del failures[:-16384]; after -= 1
        before.append(line)
    code = proc.wait()
    phase('check_'+str(cfg.get('check_index', -1) if cfg.get('check_index', -1) >= 0 else len(checks)), start)
    checks.append({'exit_code':code,'duration_ms':int((time.monotonic()-start)*1000),'output':(failures+b'\n'+tail).decode('utf-8','replace')})
# The immutable receipt cannot know the asynchronous save duration yet.
phases['cache_save'] = None if cfg.get('cache_key') and cfg.get('cache_save', True) else 0
with open(root+'/result.json','w') as f: json.dump({'checks':checks, 'phases':phases},f)
try:
    with open(root+'/output.log','wb') as f: f.write(output+b'\n'+('\n'.join(phase_lines)+'\n').encode())
    transfer('output.log',root+'/output.log',True)
except Exception as err:
    print('Warning: output.log unavailable: '+str(err), file=sys.stderr)
transfer('result.json',root+'/result.json',True)
cache_start = time.monotonic()
if cfg.get('cache_key') and cfg.get('cache_save', True): save_cache()
phase('cache_save', cache_start)
# Never recreate remote landing objects after Release. Final cache timing is
# retained in Cloud Build logs and these local build files, not the receipt.
with open(root+'/output.log','ab') as f: f.write((phase_lines[-1]+'\n').encode())
with open(root+'/result.json','w') as f: json.dump({'checks':checks, 'phases':phases},f)
`
