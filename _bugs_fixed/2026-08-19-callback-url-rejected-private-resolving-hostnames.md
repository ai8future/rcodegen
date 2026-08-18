# Callback URLs rejected private-resolving hostnames

**Fixed in 4.3.2.** `pkg/server/openai/asyncruns.go`.

## Symptom

Submitting a chat completion with a Windmill resume URL came back `400 invalid_callback_url`:

```
http://windmill.10.0.4.224.nip.io/api/w/aows/jobs_u/resume/...
```

The same host by address — `http://10.0.4.224/...` — was accepted. This broke the
submit-and-suspend integration that 4.3.0 shipped and documented: a Windmill flow
step cannot use its own resume URL as a callback if that URL names the ingress
instead of numbering it, and behind an ingress it always names it.

## Cause

`newCallbackTarget` allowed plain http only when `isPrivateOrLoopbackHost` said
so, and that function never resolved anything:

```go
ip := net.ParseIP(h)
if ip == nil {
    return false   // any hostname that is not *.localhost
}
```

So the rule was really "IP literal, or a name ending in `.localhost`", not "a host
inside the private network". `windmill.10.0.4.224.nip.io` resolves to 10.0.4.224
and was refused anyway.

## Why resolving at submit time is not enough

The naive fix — resolve during validation and accept if the answer is private — is
a time-of-check/time-of-use hole. An async run is submitted, runs for minutes or
hours, and only then is the callback POSTed. Whoever controls the name can answer
private at validation and public at delivery, and the payload (the completion, the
artifacts, and the caller's `callback_headers`, which routinely carry a bearer
token) goes to an attacker over plaintext. The long gap between the two is not
incidental to callback mode; it is the whole feature.

## Fix

Two layers, with the security in the second:

1. **Submit time** (`checkPlaintextHost`) resolves the hostname under a 2s budget
   and rejects unless every address is loopback or RFC1918. This is only so a bad
   callback URL fails fast on the caller's own connection. It is explicitly
   documented as feedback, not enforcement.
2. **Dial time** (`dialPlaintextCallback`) is the control. The plaintext delivery
   client's `DialContext` resolves the host again at the moment of connection,
   requires *every* returned address to pass, and then dials a vetted IP directly
   rather than letting the transport re-resolve the name — so the address that was
   checked is the address that is connected to. The URL still carries the name, so
   the receiver sees the `Host` header it expects. A rebinding host fails the
   connection; nothing reaches the network.

Also closed alongside it:

- **Redirects are no longer followed** on either scheme (`CheckRedirect` returns
  an error). A private receiver answering `302` to a public one would otherwise
  hand the payload past the policy. Resume URLs never redirect.
- **Link-local is refused** — `169.254.169.254` is the cloud metadata endpoint,
  the classic target for a hostile callback URL. Unspecified and multicast too.

## Regression cover

`pkg/server/openai/asyncruns_test.go`. Resolution and dialing go through
package-level hooks (`callbackLookupIP`, `callbackDialIP`), so no test touches
real DNS. The rebinding test asserts through the dial hook that the public address
was never dialed. Both new controls were mutation-checked: stubbing out the
dial-time check makes the rebinding test report three dials to `203.0.113.10`, and
allowing redirects makes the redirect test report the payload handed on.

## Note for future changes

Every delivery — a finished run's callback and the `server_shutdown` notice —
funnels through `asyncRuns.postOnce`, which picks the client via `clientFor`. If a
third delivery path is ever added, route it through there rather than building its
own `http.Client`, or the policy has a way around it.
