# SPDX-License-Identifier: Apache-2.0
# Copyright 2024 LMSYS (lm-sys/RouteLLM), Apache License 2.0 (see LICENSE in this directory).
"""Minimal RouteLLM inference code, vendored for the offline Go/PyTorch parity checks.

Source: https://github.com/lm-sys/RouteLLM at commit
0b64fdafe049e596a3f5657c219329f24af24198 (main head, 2024-08-10).

Files and changes (Apache-2.0 section 4(b)):

- ``routers.py``: ``Router`` (``route`` and ``calculate_strong_win_rate``), ``BERTRouter`` and
  ``MatrixFactorizationRouter`` copied from ``routellm/routers/routers.py``. The other routers,
  their imports and ``NAME_TO_CLS`` were removed.
- ``matrix_factorization/model.py``: copied from
  ``routellm/routers/matrix_factorization/model.py``. The OpenAI embedding call in
  ``MFModel.forward`` is replaced by a lookup in ``RECORDED_EMBEDDINGS`` (recorded vectors, so no
  network access), and the ``OPENAI_CLIENT`` import was removed.

Nothing else is changed. This package is not imported by the Go code.
"""

ROUTELLM_COMMIT = "0b64fdafe049e596a3f5657c219329f24af24198"
