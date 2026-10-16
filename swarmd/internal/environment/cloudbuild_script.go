package environment

import (
	"encoding/base64"
	"encoding/json"
)

// cloudBuildScript requires bash, python3, curl, tar, git and GNU timeout in the
// chosen base image, plus useradd and runuser. Infrastructure errors escape;
// check exit codes are data.
func cloudBuildScript(bucket, prefix, sha string) string {
	data, _ := json.Marshal(map[string]string{"bucket": bucket, "prefix": prefix, "sha256": sha})
	return "set -euo pipefail\nIMAGE_PATH=\"$PATH\"\nexport IMAGE_PATH\nexport GOPATH=/workspace/go\nexport GOCACHE=/workspace/.cache/go-build\nexport GOMODCACHE=/workspace/go/pkg/mod\npython3 - '" + base64.StdEncoding.EncodeToString(data) + "' <<'SWARM_PY'\n" + cloudBuildPython + "\nSWARM_PY\n"
}

const cloudBuildPython = `import base64, collections, hashlib, json, os, pwd, re, subprocess, sys, time, urllib.parse, urllib.request
cfg = json.loads(base64.b64decode(sys.argv[1]))
root = '/workspace'
def token():
    req = urllib.request.Request('http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token', headers={'Metadata-Flavor':'Google'})
    with urllib.request.urlopen(req, timeout=30) as r: return json.load(r)['access_token']
def transfer(name, path, upload=False):
    obj = cfg['prefix'] + name
    if upload:
        url = 'https://storage.googleapis.com/upload/storage/v1/b/' + urllib.parse.quote(cfg['bucket'], safe='') + '/o?uploadType=media&name=' + urllib.parse.quote(obj, safe='')
    else:
        url = 'https://storage.googleapis.com/storage/v1/b/' + urllib.parse.quote(cfg['bucket'], safe='') + '/o/' + urllib.parse.quote(obj, safe='') + '?alt=media'
    # Token is passed through stdin configuration, not argv or output.
    args = ['curl','--fail','--silent','--show-error','--max-time','300','--config','-']
    args += ['-X','POST','--data-binary','@'+path] if upload else ['-o',path]
    subprocess.run(args + [url], input=('header = "Authorization: Bearer '+token()+'"\n').encode(), check=True, stdout=subprocess.DEVNULL)
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
for path in cache_env.values(): os.makedirs(path, exist_ok=True)
def login_command(command):
    return 'export PATH="$IMAGE_PATH:$PATH"; ' + command
for command in request['definition'].get('Setup') or []:
    subprocess.run(['bash','-lc',login_command(command)],env=env,check=True,timeout=request['timeout'])
subprocess.run(['git','init'],check=True)
subprocess.run(['git','add','-A'],check=True)
subprocess.run(['git','-c','user.name=Swarm Landing','-c','user.email=landing@swarm.local','commit','--allow-empty','-m','Landing tree'],check=True)
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
    checks.append({'exit_code':code,'duration_ms':int((time.monotonic()-start)*1000),'output':(failures+b'\n'+tail).decode('utf-8','replace')})
with open(root+'/result.json','w') as f: json.dump({'checks':checks},f)
try:
    with open(root+'/output.log','wb') as f: f.write(output)
    transfer('output.log',root+'/output.log',True)
except Exception as err:
    print('Warning: output.log unavailable: '+str(err), file=sys.stderr)
transfer('result.json',root+'/result.json',True)
`
