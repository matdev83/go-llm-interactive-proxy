# Original user financial contracts

Verbatim from the user's review request; spelling and emphasis are preserved. C01–C11 in the specification correspond to entries 1–11 below. Operational interpretations and limits of guarantees are explicit in requirements.md and design.md, not silently substituted in this source.

## C01

> Proxy user is charged for the traffic it sends. This involves being charged on all B-legs created in relation to the user submitted A-leg messages. One A-leg may be related to multiple B-legs being created by the proxy ie failover routing, thinker-model requests, auxiliary requests, requests submitted to the remote and resulting in proxy operator being billed, even if requests eventually failed.

## C02

> Users cannot spend more than they afford.

## C03

> Rouge user cannot submit 1000 of requests in very short time when they cannot afford handling of all 1000 requests (calculated based on full pessimistic max spend per request) to force financial harm to the proxy operator.

## C04

> In concurrent usage scenarios if ANY of the ongoing user requests hit out-of-money condition, all other ongoing requests of the same user must get immediately halted.

## C05

> Limits are applied BEFORE handling any user request, based on the pessimistic scenario. Pessimistic scenario is: full cache write cost based on current request size + cost of completion based on maximum possible model output (as per models.dev catalog, with optional fallback to user-submitted max tokens limit) with assumption of full cache miss.

## C06

> Each new user request must be subject to the same pessimistic treatment, AFTER accounting for the pessimistic max costs of all currently ongoing user requests.

## C07

> Errors never prevent user billing. Eventual errors are handled in a way that warrants that failing billing attempts are repeated until successful, NEVER DROPPED.

## C08

> Server survives load spikes in the database layer and all billing-related events are properly executed after re-tries.

## C09

> Users who cannot affort given request being handled based on the pessimistic calculation, receive clear readable error message, OpenRouter-style "You can afford only X completion tokens, while maximum model output size is higher" and this info is roughly based on the formula: max allowed user spend = user account balance - (pessimistic cost of all ongoing/concurrent user requests + pessimistic cost of this new request).

## C10

> Accounting ledger(s) are used. Each operation is double-sided.

## C11

> Billing operations consist of at least: debit user balance + log user spend + log operator's income.
