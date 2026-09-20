"""Local acceptance fixture, not a production receiver. State survives restarts."""
import hashlib, hmac, json, os, sqlite3, time, threading, uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

STATE = '/state'
def db():
    return sqlite3.connect(STATE + '/inbox.db', timeout=10)
with db() as c:
    c.executescript('''CREATE TABLE IF NOT EXISTS inbox (tenant TEXT, id TEXT, body TEXT, signature TEXT, processed INTEGER DEFAULT 0, PRIMARY KEY(tenant,id));
    CREATE TABLE IF NOT EXISTS effects (tenant TEXT, id TEXT, PRIMARY KEY(tenant,id));
    CREATE TABLE IF NOT EXISTS attempts (status INTEGER);
    CREATE TABLE IF NOT EXISTS worker_failures (n INTEGER);''')

def worker():
    while True:
        try:
            with db() as c:
                c.execute('BEGIN IMMEDIATE')
                row = c.execute('SELECT tenant,id FROM inbox WHERE processed=0 LIMIT 1').fetchone()
                if row:
                    c.execute('INSERT INTO effects VALUES(?,?)', row)
                    if os.path.exists(STATE + '/worker-fail'):
                        raise RuntimeError('injected failure before processed marker')
                    c.execute('UPDATE inbox SET processed=1 WHERE tenant=? AND id=?', row)
        except RuntimeError:
            with db() as c: c.execute('INSERT INTO worker_failures VALUES(1)')
        time.sleep(.1)

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def do_GET(self):
        with db() as c:
            result = {name: c.execute('SELECT count(*) FROM '+name).fetchone()[0] for name in ['inbox','effects','worker_failures']}
            result['attempts'] = [r[0] for r in c.execute('SELECT status FROM attempts')]
            result['pending'] = c.execute('SELECT count(*) FROM inbox WHERE processed=0').fetchone()[0]
            row = c.execute('SELECT body,signature FROM inbox LIMIT 1').fetchone()
            if row: result.update(body=row[0], signature=row[1])
        self.send_response(200); self.send_header('Content-Type','application/json'); self.end_headers()
        self.wfile.write(json.dumps(result).encode())
    def do_POST(self):
        size = int(self.headers.get('Content-Length', '0'))
        if size <= 0 or size > 1048576: self.send_error(400); return
        body = self.rfile.read(size)
        status = 401
        try:
            config = json.load(open(STATE+'/config.json'))
            header = self.headers.get('X-Wallet-Signature','')
            t, signature = header.split(',')
            assert t.startswith('t=') and signature.startswith('v1=')
            assert abs(time.time()-int(t[2:])) <= 300
            expected = hmac.new(config['secret'].encode(),t[2:].encode()+b'.'+body,hashlib.sha256).hexdigest()
            assert hmac.compare_digest(expected,signature[3:])
            event=json.loads(body)
            tenant, identity = str(uuid.UUID(event['tenant_id'])), str(uuid.UUID(event['id']))
            assert tenant==config['tenant'] and uuid.UUID(identity).int and event['type']
            if os.path.exists(STATE+'/reject'): status=503
            else:
                with db() as c:
                    c.execute('INSERT INTO inbox(tenant,id,body,signature) VALUES(?,?,?,?) ON CONFLICT DO NOTHING', (tenant,identity,body.decode(),header))
                status=200
        except (ValueError,KeyError,AssertionError): pass
        except (OSError,sqlite3.Error): status=503
        with db() as c: c.execute('INSERT INTO attempts VALUES(?)',(status,))
        self.send_response(status); self.end_headers()

threading.Thread(target=worker,daemon=True).start()
ThreadingHTTPServer(('0.0.0.0',8089), Handler).serve_forever()
