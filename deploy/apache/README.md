# Apache virtual host for go.sstools.co

This directory contains the Apache (`httpd`) virtual host configuration that
terminates TLS and reverse-proxies all traffic for `go.sstools.co` to the Go
service listening on `127.0.0.1:8080`.

Tested on **Amazon Linux 2023** with `httpd` and `mod_ssl`.

## Install

On AL2023, drop the file into `/etc/httpd/conf.d/` — all `.conf` files in that
directory are loaded automatically. There is no `a2ensite` command:

```bash
sudo cp go.sstools.co.conf /etc/httpd/conf.d/ && sudo systemctl reload httpd
```

The proxy and SSL modules are included in the `httpd` and `mod_ssl` packages
installed during server setup — no separate module-enable step is needed.

## Obtain the Let's Encrypt certificate

The virtual host references certificate paths under
`/etc/letsencrypt/live/go.sstools.co/`. Obtain them with certbot:

```bash
sudo certbot --apache -d go.sstools.co
```

## Notes

- The `/api/events` route is proxied with `flushpackets=on` and **must** appear
  before the wildcard `ProxyPass /` line. Apache evaluates `ProxyPass`
  directives top-to-bottom, so if the wildcard came first it would match the SSE
  path and response buffering would not be disabled.
- `/mcp` (the MCP server for AI agents, see `docs/mcp.md`) is proxied the same
  way, ahead of the wildcard. The wildcard alone would work, because the
  server is stateless and answers plain JSON. The `/mcp` block only adds
  `flushpackets=on`, `SetEnv no-gzip 1` and `timeout=120`, so a streamed
  (SSE) response would not be buffered, compressed or cut off at the default
  60 seconds. `ProxyPreserveHost On` matters here: the server's cross-origin
  check compares the `Origin` header to `Host`.
- The same file serves every instance. For go.opencircuitsf.com,
  go.dustalmanac.com, go.coupledozen.com and go.shouteureka.com, change
  `ServerName` and the two certificate paths, and keep the port that
  instance's service listens on.
- Check a change with `sudo apachectl configtest` before reloading.
