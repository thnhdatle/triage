# PII test data

Regenerate the labelled data of the `piiregex` detectors and look up which formats they detect.
Only the formats listed here are protected. Names and locations need the optional NER detector,
which is English only.

## Files

| File | Content |
|---|---|
| `labelled.jsonl` | 1,400 strings with PII (140 per type; English, Korean, Vietnamese; some hold two values) |
| `negatives.jsonl` | 1,000 PII-free prompts (code, prose, dates, prices, versions, order ids, hashes, KO/VI text, near-miss values) |
| `REPORT.md` | per-detector precision/recall, rewritten by `go test ./internal/adapter/piiregex -run PrecisionRecall -update` |

One JSON object per line:

```json
{"text": "...", "spans": [{"start": 0, "end": 4, "type": "email"}], "lang": "en"}
```

- `start`, `end`: **byte** offsets into the UTF-8 `text` (`text[start:end]` is the value).
- `lang`: dominant language (`en`, `ko`, `vi`). Negatives mix languages.

## Regenerate

```sh
go run ./internal/adapter/piiregex/internal/gen/cmd/piigen -out testdata/pii   # deterministic, seed 20261019
```

A test fails when the committed files differ from the generator output.

## Types and formats

Types are lower snake_case. The placeholder uses the upper-cased type.

### Checksum detectors

| Type | Format |
|---|---|
| `credit_card` | 13-19 digits, plain or in groups separated by single spaces or dashes (multi-group numbers start with a 4-digit group: 4-4-4-4, 4-6-5, 4-6-4); Luhn valid; plausible issuer prefix and length (Visa, Mastercard, Amex, Discover, JCB, Diners, UnionPay, Maestro). A window that continues a longer digit-group sequence is ignored. |
| `iban` | Country code, 2 check digits, BBAN; length per country table (80 countries); ISO 7064 mod 97 = 1; plain or with single spaces. |

### Structural detectors

| Type | Format |
|---|---|
| `email` | `local@domain.tld` (ASCII local part with `._%+-`, TLD of 2+ letters). |
| `ip` | IPv4 dotted quad (octets 0-255, no leading zeros, not preceded by `v`, not part of a longer dotted number) and IPv6 (any form `net/netip` accepts, no zone; Python slices such as `x[::2]` are ignored). |
| `us_ssn` | `AAA-GG-SSSS` or `AAA GG SSSS` (same separator); area not 000, 666, 900-999; group not 00; serial not 0000. Undashed nine digits are not detected. |
| `secret` | Prefixes `sk-` (20+ key characters including a digit and a letter), `AKIA` + 16, `ghp_`/`gho_`/`ghu_`/`ghs_`/`ghr_` + 36+, `xox[abprs]-` + 10+, and JWTs (`eyJ….eyJ….sig`). The prefix must not be glued to a preceding letter, digit or `_`. |
| `kr_rrn` | `YYMMDD-GNNNNNN` with dash or space, valid calendar date, gender digit 1-8 (1,2,5,6 = 19xx; 3,4,7,8 = 20xx). 13 undashed digits also need a context word within 32 characters or a valid checksum. A valid checksum (weights 2-9,2-5; check = (11 - sum mod 11) mod 10) only raises confidence: numbers issued since October 2020 have a random tail, so dashed numbers with an invalid checksum are still found. |

### Context-anchored detectors

A match counts only with a context word within the **32 characters (runes)** right before or
after the value. Matching is case-insensitive on NFC text. The word must be bounded by
non-ASCII-alphanumeric characters (`hotel` does not count as `tel`).

| Type | Formats | Context words |
|---|---|---|
| `phone` | E.164 `+<country><digits>` (8-15 digits, spaces, dots, dashes, parentheses allowed): **no context needed**. KR `010-1234-5678` (also `011`, `016`-`019`, landlines such as `02-123-4567`, `031-123-4567`; dash, space, dot or none). VN `0912 345 678` (mobile `03x`, `05x`, `07x`, `08x`, `09x`, 10 digits; landlines `024 3825 1234`). US NANP `(415) 555-2671`, `415-555-2671`, `415.555.2671`, `1-415-555-2671` (area code and exchange start with 2-9). | `phone`, `tel`, `telephone`, `mobile`, `cell`, `cellphone`, `whatsapp`, `전화`, `전화번호`, `휴대폰`, `휴대전화`, `핸드폰`, `연락처`, `điện thoại`, `dien thoai`, `sđt`, `sdt`, `di động`, `hotline`, `zalo`, `số máy` |
| `vn_cccd` | 12 digits; province code 001-096; century/gender digit (4th digit) 0-3. | `cccd`, `căn cước`, `can cuoc`, `cmnd`, `cmt`, `số định danh`, `so dinh danh`, `citizen id`, `chứng minh` |
| `kr_rrn` (undashed only) | 13 plain digits, see above. | `주민등록번호`, `주민번호`, `주민등록`, `rrn`, `resident registration` |

### Heuristic (disabled by default)

| Type | Formats |
|---|---|
| `street_address` | English `221 Baker Street` (number, 1-3 capitalised words, street suffix such as Street, St, Avenue, Ave, Road, Rd, Boulevard, Blvd, Lane, Drive, Court, Way, Place); Vietnamese `12/5 đường Lê Lợi` (đường, phố, ngõ, ngách, hẻm + capitalised words); Korean `서울특별시 강남구 테헤란로 123` (optional 도/시/구/군 prefix, name ending in 대로/로/길, number). |

## Overlaps and known ambiguity

- Overlaps resolve by class (checksum, structural, context, heuristic), then by the longer span.
- A 13-digit valid card number that is also a plausible RRN is reported as `credit_card`. The
  generator avoids such collisions.
- 13-digit epoch timestamps can rarely look like an undashed RRN with a valid checksum. The
  negatives include some.

## Thresholds

| Metric | Threshold |
|---|---|
| Recall, checksum and structural detectors | >= 0.99 |
| Recall, context and street-address detectors | >= 0.95 |
| False-positive rate on `negatives.jsonl` | <= 2% |
