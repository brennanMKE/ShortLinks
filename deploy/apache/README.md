# Apache virtual host for go.sstools.co

This directory contains the Apache 2 virtual host configuration that terminates
TLS and reverse-proxies all traffic for `go.sstools.co` to the Go service
listening on `127.0.0.1:8080`.

## Required Apache modules

Enable the proxy and SSL modules before installing the site:

```bash
sudo a2enmod proxy proxy_http ssl
```

## Install

```bash
sudo cp go.sstools.co.conf /etc/apache2/sites-available/ && sudo a2ensite go.sstools.co && sudo systemctl reload apache2
```

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
