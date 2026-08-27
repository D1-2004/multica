# Runtime model pricing sources

`dt-fde-multica-model-pricing.json` stores USD rates per million tokens. The managed catalog uses public list prices, not temporary promotions or subscription-plan quotas.

Qwen CNY prices below use the 2026-08-24 central parity rate `1 USD = 6.7841 CNY`. Rates with request-size tiers use the first tier because Multica's task usage rows aggregate multiple model calls and cannot reconstruct each request's prompt size. `base_tier_max_input_tokens` makes that estimate explicit in the model picker.

| Model | Source list price | Managed USD input / output | Cache read / write | Source |
|---|---:|---:|---:|---|
| `qwen3.8-max` | CNY 12 / 36 | 1.768842 / 5.306526 | 0.221105 / 2.211052 | [Alibaba Cloud Model Studio](https://help.aliyun.com/zh/model-studio/qwen3-8-max) |
| `qwen3.7-plus` | CNY 2 / 8, input <=256K | 0.294807 / 1.179228 | 0.058961 / 0.368509 | [Alibaba Cloud Model Studio](https://help.aliyun.com/zh/model-studio/qwen3-7-plus) |
| `qwen3.7-max` | CNY 12 / 36 | 1.768842 / 5.306526 | 0.353768 / 2.211052 | [Alibaba Cloud Model Studio](https://help.aliyun.com/zh/model-studio/qwen3-7-max) |
| `qwen3-coder-480b-a35b-instruct` | CNY 6 / 24, input <=32K | 0.884421 / 3.537684 | 0.884421 / 0.884421 | [Alibaba Cloud Model Studio](https://help.aliyun.com/zh/model-studio/qwen3-coder-480b-a35b-instruct) |
| `claude-sonnet-4-6` | USD 3 / 15 | 3 / 15 | 0.3 / 3.75 | [Anthropic](https://platform.claude.com/docs/en/about-claude/pricing) |
| `qwen3.5-plus` | CNY 0.8 / 4.8, input <=128K | 0.117923 / 0.707537 | 0.011792 / 0.147403 | [Alibaba Cloud Model Studio](https://help.aliyun.com/zh/model-studio/qwen3-5-plus) |
| `kimi-k2.5` | USD 0.6 / 3 | 0.6 / 3 | 0.1 / 0.6 | [Kimi API Platform](https://platform.kimi.ai/docs/pricing/chat-k25) |
| `glm-5` | USD 1 / 3.2 | 1 / 3.2 | 0.2 / 0 | [Z.AI](https://docs.z.ai/guides/overview/pricing) |
| `qwen3-max-preview` | CNY 6 / 24, input <=32K | 0.884421 / 3.537684 | 0.176884 / 0.884421 | [Alibaba Cloud Model Studio](https://help.aliyun.com/zh/model-studio/model-qwen3-max) |

For models without a separate cache-write charge, cache write mirrors ordinary input. GLM-5's published cached-input storage price is currently free, so its cache-write rate is zero. Provider-reported cost always overrides the catalog estimate.
