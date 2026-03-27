# Security Policy & Incident Response Plan

## Reporting a Vulnerability

If you discover a security vulnerability in CUDly, **do not open a public GitHub issue**.

Contact the maintainers directly via email (see repository settings for contact). Provide:

- A description of the vulnerability and its potential impact
- Steps to reproduce
- Any suggested mitigations

We commit to acknowledging reports within 48 hours and providing an initial assessment within 7 days.

---

## Incident Response Plan

### Severity Definitions

| Level | Criteria | Example |
| ----- | -------- | ------- |
| **P1 – Critical** | Active exploitation, data breach in progress, or credential compromise | Leaked API keys found in logs; active data exfiltration |
| **P2 – High** | Vulnerability exploitable without authentication, or significant data exposure | Unauthenticated endpoint bypassed; PII query result in error log |
| **P3 – Medium** | Exploitable with authentication, or limited impact | Authenticated user can access another user's read-only data |
| **P4 – Low** | Theoretical risk, no active exploitation | Verbose error message reveals stack trace |

### Response Timeline

| Severity | Detection → Triage | Triage → Containment | Resolution |
| -------- | ----------------- | -------------------- | ---------- |
| P1 | 15 min | 1 hour | 4 hours |
| P2 | 1 hour | 4 hours | 24 hours |
| P3 | 4 hours | 24 hours | 7 days |
| P4 | 24 hours | 7 days | 30 days |

### Response Phases

#### 1. Detection & Triage

- Confirm the incident is real (not a false alarm)
- Determine severity using the table above
- Assign an Incident Commander (IC)
- Open a private incident channel (Slack/Discord/email thread)

#### 2. Containment

- Isolate affected systems (disable credentials, block IPs, take service offline if necessary)
- Preserve logs and evidence **before** making changes
- Notify stakeholders per the communication plan below

#### 3. Eradication

- Remove the root cause (patch code, rotate credentials, fix misconfiguration)
- Verify the fix does not introduce new issues
- Deploy to staging and validate

#### 4. Recovery

- Deploy fix to production
- Gradually restore service (canary if possible)
- Monitor closely for 24 hours post-recovery

#### 5. Post-Mortem

- Within 5 business days of resolution
- Document: timeline, root cause, impact, fix, action items
- Update runbooks and monitoring based on learnings
- Share a sanitised summary with affected customers if applicable

---

## Emergency Contacts

| Resource | Contact / URL |
| -------- | ------------- |
| AWS Support | <https://console.aws.amazon.com/support> (create case) |
| Azure Support | <https://portal.azure.com> → Support + troubleshooting |
| GCP Support | <https://console.cloud.google.com/support> |
| GitHub Security Advisories | <https://github.com/advisories> |
| Domain Registrar | (update with your registrar's emergency contact) |

---

## Communication Plan

### Internal

- Notify all engineers with production access immediately for P1/P2
- Use a dedicated private channel; do not discuss in public channels

### External (GDPR Art. 33/34)

- For personal data breaches: notify the relevant Data Protection Authority **within 72 hours** of becoming aware
- Notify affected data subjects without undue delay if the breach is likely to result in high risk to their rights

### Customer Notification

- For P1/P2 incidents affecting customer data: notify affected customers within 24 hours of confirmed impact
- Include: what happened, what data was affected, what actions customers should take

---

## Post-Incident Checklist

- [ ] Incident timeline documented
- [ ] Root cause identified and fixed
- [ ] All affected credentials rotated
- [ ] All affected sessions invalidated
- [ ] Monitoring/alerting updated to detect recurrence
- [ ] Post-mortem written and shared
- [ ] GDPR notification filed (if applicable)
- [ ] Runbooks updated
- [ ] Action items tracked in issue tracker

---

## Runbooks

See [.github/runbooks/](.github/runbooks/) for step-by-step procedures:

- [Credential Compromise](.github/runbooks/credential-compromise.md)
- [Data Breach Response](.github/runbooks/data-breach-response.md)
- [DDoS Mitigation](.github/runbooks/ddos-mitigation.md)
- [Compromised Dependency](.github/runbooks/compromised-dependency.md)
