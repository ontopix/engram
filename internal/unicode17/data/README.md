# Unicode 17.0.0 data provenance

`tables_generated.go` is generated from the Unicode Character Database
17.0.0. The generator verifies these exact inputs before producing output:

| File | Source | SHA-256 |
|---|---|---|
| `UnicodeData.txt` | `https://www.unicode.org/Public/17.0.0/ucd/UnicodeData.txt` | `2e1efc1dcb59c575eedf5ccae60f95229f706ee6d031835247d843c11d96470c` |
| `DerivedNormalizationProps.txt` | `https://www.unicode.org/Public/17.0.0/ucd/DerivedNormalizationProps.txt` | `71fd6a206a2c0cdd41feb6b7f656aa31091db45e9cedc926985d718397f9e488` |
| `CompositionExclusions.txt` | `https://www.unicode.org/Public/17.0.0/ucd/CompositionExclusions.txt` | `2f239196ef3b5b61db5cc476e9bd80f534d15aa1b74e1be1dea5d042a344c85f` |
| `CaseFolding.txt` | `https://www.unicode.org/Public/17.0.0/ucd/CaseFolding.txt` | `ff8d8fefbf123574205085d6714c36149eb946d717a0c585c27f0f4ef58c4183` |
| `NormalizationTest.txt` | `https://www.unicode.org/Public/17.0.0/ucd/NormalizationTest.txt` | `5019ffd530751a741900c849c0e010332f142a3612234639bd200b82138a87db` |

Download the files into one directory, then run:

```text
go run ./internal/unicode17/cmd/gentables -ucd-dir <directory> -output internal/unicode17/tables_generated.go
```

The generated tables and source data are covered by the Unicode License v3,
reproduced in `LICENSE.txt`.
