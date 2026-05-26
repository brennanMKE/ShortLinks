# Email Infrastructure Setup

This document covers the EC2 and Route 53 configuration required to send transactional email (magic links, account recovery) from ShortLinks reliably and without landing in spam.

---

## Why Direct SMTP from EC2 Fails

AWS blocks outbound port 25 on all EC2 instances by default to prevent spam. Even if unblocked, EC2 IP addresses are frequently listed in spam blocklists (Spamhaus PBL, etc.) because the address space is shared and historically abused. Sending email directly from an EC2 instance to a major inbox provider (Gmail, Apple, Outlook) will result in deliverability failures.

The correct approach is to route all outbound email through **AWS Simple Email Service (SES)**, which handles IP reputation, feedback loops, and bounce management. SES SMTP endpoints use port 587 (STARTTLS) or 465 (TLS), which are not blocked.

---

## Recommended Architecture

```
Go service (EC2)
    |
    | SMTP / SES API
    v
AWS SES  ────────────────────────────────────────────►  Recipient inbox
    |
    | DKIM signing, bounce/complaint handling
    v
Route 53 (SPF, DKIM CNAMEs, DMARC, custom MAIL FROM)
```

The Go service sends via SES. SES signs the message with DKIM, enforces SPF alignment via the custom MAIL FROM subdomain, and reports DMARC compliance. Route 53 hosts all the DNS records that receiving mail servers check before accepting the message.

---

## Step 1 — Verify the Domain in SES

1. Open **AWS SES → Verified identities → Create identity**
2. Choose **Domain**, enter `sstools.co`
3. Enable **Easy DKIM** — SES generates three CNAME records
4. Check **Assign a default configuration set** (for bounce/complaint tracking later)
5. SES presents the DNS records to add — proceed to Step 2

---

## Step 2 — Route 53 DNS Records

All records below are added to the `sstools.co` hosted zone in Route 53.

### DKIM (3 CNAME records — values provided by SES)

SES generates unique selectors per domain. The format is:

| Name | Type | Value |
|------|------|-------|
| `<selector1>._domainkey.sstools.co` | `CNAME` | `<selector1>.dkim.amazonses.com` |
| `<selector2>._domainkey.sstools.co` | `CNAME` | `<selector2>.dkim.amazonses.com` |
| `<selector3>._domainkey.sstools.co` | `CNAME` | `<selector3>.dkim.amazonses.com` |

Copy the exact values from the SES console — do not construct them manually.

### SPF (TXT record on custom MAIL FROM subdomain)

Rather than adding SES to the SPF record on the root domain, use a **custom MAIL FROM subdomain**. This achieves SPF alignment under DMARC without touching any existing SPF record on `sstools.co`.

In SES → verified identity → **Custom MAIL FROM domain**, set it to `mail.sstools.co`.

SES then requires two records:

| Name | Type | Value |
|------|------|-------|
| `mail.sstools.co` | `MX` | `10 feedback-smtp.us-east-1.amazonses.com` *(use your SES region)* |
| `mail.sstools.co` | `TXT` | `v=spf1 include:amazonses.com ~all` |

The `From:` header will show `noreply@sstools.co` (or `go.sstools.co`), while the envelope sender (`MAIL FROM`) will be `bounce+...@mail.sstools.co`. Both are under `sstools.co`, satisfying DMARC alignment.

### DMARC (TXT record)

| Name | Type | Value |
|------|------|-------|
| `_dmarc.sstools.co` | `TXT` | `v=DMARC1; p=quarantine; adkim=s; aspf=s; rua=mailto:dmarc-reports@sstools.co; ruf=mailto:dmarc-reports@sstools.co; fo=1` |

| Tag | Meaning |
|-----|---------|
| `p=quarantine` | Failing messages go to spam rather than being rejected outright — safer during ramp-up; change to `p=reject` once deliverability is confirmed |
| `adkim=s` | Strict DKIM alignment — the `d=` tag in the DKIM signature must exactly match the `From:` domain |
| `aspf=s` | Strict SPF alignment — the MAIL FROM domain must exactly match the `From:` domain |
| `rua=` | Aggregate report destination (daily XML summaries from receiving mail servers) |
| `ruf=` | Forensic report destination (individual failure samples) |
| `fo=1` | Generate forensic reports on any alignment failure |

> **Note:** The `rua`/`ruf` addresses (`dmarc-reports@sstools.co`) need to be able to receive email. If `sstools.co` has no inbound mail setup, use an external service like [DMARC Digests](https://dmarcdigests.com) or [Postmark's DMARC analyzer](https://dmarc.postmarkapp.com) and point the `rua` address there instead.

### Summary of all records

| Name | Type | Value | Purpose |
|------|------|-------|---------|
| `<sel1>._domainkey.sstools.co` | CNAME | *(from SES)* | DKIM |
| `<sel2>._domainkey.sstools.co` | CNAME | *(from SES)* | DKIM |
| `<sel3>._domainkey.sstools.co` | CNAME | *(from SES)* | DKIM |
| `mail.sstools.co` | MX | `10 feedback-smtp.<region>.amazonses.com` | Custom MAIL FROM |
| `mail.sstools.co` | TXT | `v=spf1 include:amazonses.com ~all` | SPF for MAIL FROM |
| `_dmarc.sstools.co` | TXT | `v=DMARC1; p=quarantine; ...` | DMARC policy |

---

## Step 3 — Move SES Out of Sandbox

New SES accounts are in **sandbox mode**: email can only be sent to verified addresses, and daily sending volume is capped at 200 messages.

To request production access:

1. **AWS SES → Account dashboard → Request production access**
2. Select **Transactional** email type
3. Describe the use case: account registration magic links and password recovery for a self-hosted URL shortener
4. Provide the website URL (`https://go.sstools.co`)
5. Confirm bounce and complaint handling is in place (SES SNS notifications or configuration set)
6. AWS typically approves within 24 hours

---

## Step 4 — SES SMTP Credentials

The Go service authenticates to SES over SMTP using IAM-derived credentials (not your AWS root credentials).

1. **SES → SMTP settings → Create SMTP credentials**
2. This creates an IAM user with `ses:SendRawEmail` permission and generates an SMTP username and password
3. Store both values in `/etc/shortlinks/config.env` alongside the database DSN:

```env
SES_SMTP_HOST=email-smtp.us-east-1.amazonaws.com
SES_SMTP_PORT=587
SES_SMTP_USERNAME=AKI...
SES_SMTP_PASSWORD=...
EMAIL_FROM=ShortLinks <noreply@sstools.co>
```

Alternatively, use the **SES API** via the AWS SDK for Go (`github.com/aws/aws-sdk-go-v2/service/sesv2`), which uses instance IAM role credentials and avoids storing a static SMTP password.

---

## Step 5 — Bounce and Complaint Handling

Unhandled bounces and complaints will damage sender reputation and can get the SES account suspended.

1. In SES, create a **Configuration Set** (e.g., `shortlinks-transactional`)
2. Add an **SNS event destination** for `Bounce` and `Complaint` event types
3. Create an SNS topic and subscribe an SQS queue or Lambda to process events
4. At minimum: log hard bounces and complaints, and suppress those addresses from future sends

For a low-volume deployment, a simple approach is to subscribe an email address to the SNS topic and monitor it manually.

---

## Deliverability Checklist

Before sending to real users, verify each item:

- [ ] SES domain identity status is **Verified**
- [ ] All three DKIM CNAME records resolve correctly (`dig CNAME <selector>._domainkey.sstools.co`)
- [ ] Custom MAIL FROM MX and TXT records resolve (`dig MX mail.sstools.co`, `dig TXT mail.sstools.co`)
- [ ] DMARC TXT record resolves (`dig TXT _dmarc.sstools.co`)
- [ ] SES account is out of sandbox (production access granted)
- [ ] Send a test message to [mail-tester.com](https://www.mail-tester.com) — target score 9/10 or higher
- [ ] Send a test message to a Gmail address and confirm it arrives in the inbox (not spam), with a padlock icon showing DKIM pass
- [ ] View the raw message headers in Gmail → confirm `dkim=pass`, `spf=pass`, `dmarc=pass`
- [ ] Bounce/complaint SNS notifications are wired up

---

## Go Integration

The `internal/auth` package will use a thin mailer interface so the transport can be swapped in tests:

```go
type Mailer interface {
    Send(ctx context.Context, to, subject, htmlBody string) error
}
```

Production implementation sends via SES SMTP (port 587 + STARTTLS) or the SES API. Tests use a no-op or in-memory implementation that captures sent messages for assertion.

---

## Open Questions

- **From address** — `noreply@sstools.co` or `noreply@go.sstools.co`? Using the root domain (`sstools.co`) is better for DMARC alignment if you ever send from other subdomains.
- **DMARC reports inbox** — where should `rua`/`ruf` reports be delivered? Requires an address that can receive email.
- **SES region** — use the region closest to the EC2 instance to minimize latency on magic-link sends.
