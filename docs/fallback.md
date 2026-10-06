# Automatic fallback (0.3.1)

An unpinned task tries each compatible candidate at most once within the same
job ID. All candidates retain the requested image model and request parameters.

| Outcome | Next candidate? |
| --- | --- |
| Definite authentication, quota, model availability or rate rejection | Yes |
| HTTP 5xx, unless the error identifies safety or invalid input | Yes, if no image/preview was delivered |
| Explicit terminal server/upstream error in HTTP 200 JSON or an SSE error event | Yes, if no image/preview was delivered |
| Local wait interval expires while job is running | No; continue the same job |
| Transport loss without a terminal server error | No; remote result remains unknown |
| Any saved image/preview, including a partially completed batch | No; preserve output |
| Safety refusal or invalid request parameters | No |
| Explicitly pinned provider | No |

Server errors do not establish that an upstream request was never billed.
Attempts retain response metadata, request ID, `billing_unknown` and `fell_back`.
Successful fallback includes a billing warning when an earlier channel's charge
is uncertain. The client does not loop over providers or re-submit a failed job
when queried with `status` or `wait`.

Regression tests execute the compiled CLI and detached worker against controlled
HTTP/SSE servers. They cover HTTP-200 internal_server_error/EOF, 502/504, exhausted
candidates, pins, invalid input, safety refusals and a completed image followed
by a server error. They verify request counts, retained evidence and warnings.
