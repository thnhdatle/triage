# SPDX-License-Identifier: Apache-2.0
"""Build the tokenizer corpus and emit reference token ids.

Two subcommands:

``corpus``
    Writes ``testdata/tokenizer/corpus.jsonl`` (300 prompts) from the texts defined in this
    module. The output is deterministic: no randomness, no network, stable order. Every text
    is written for this project; none is taken from a licensed dataset.

``reference``
    Encodes every corpus text with Hugging Face ``tokenizers`` and writes
    ``reference_bert.jsonl`` (XLM-R Unigram, ``<s>`` ... ``</s>``, every prompt) and
    ``reference_ner.jsonl`` (WordPiece, ``[CLS]`` ... ``[SEP]``, prompts with ``lang == "en"``).
    Truncation and padding configured in the ``tokenizer.json`` are disabled: the reference
    holds the full id sequence and truncation is tested separately.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import unicodedata
from collections.abc import Iterable, Sequence
from dataclasses import dataclass
from pathlib import Path

from tokenizers import Tokenizer

REPO_ROOT = Path(__file__).resolve().parents[3]
DEFAULT_OUT_DIR = REPO_ROOT / "testdata" / "tokenizer"
DEFAULT_TOKENIZER_DIR = Path("~/.cache/rayld/triage/tokenizers")
CORPUS_FILE = "corpus.jsonl"
NER_LANG = "en"
CORPUS_SIZE = 300
LONG_MIN_IDS = 515  # > 512 content tokens once <s> and </s> are added
DEFAULT_GO_TABLES = REPO_ROOT / "internal" / "platform" / "hftok" / "tables.go"
_SURROGATES = (0xD800, 0xDFFF)
_MAX_R16 = 0xFFFF
_ISOLATED_PIECES = 3


@dataclass(frozen=True)
class Prompt:
    """One corpus line: ``{"id", "category", "text", "lang"}``."""

    id: str
    category: str
    text: str
    lang: str


# --- base texts ------------------------------------------------------------------------------

EN = (
    "What is the capital of Australia, and why isn't it Sydney?",
    "Barack Obama was born in Honolulu, Hawaii, on August 4, 1961.",
    "Summarize the following email from Maria Garcia at Acme Corp. in two sentences.",
    "Can you explain how a transformer model uses self-attention?",
    "I'm flying from New York to London next Tuesday; what should I pack?",
    "The Eiffel Tower is 330 metres tall and was completed in 1889.",
    "Write a haiku about autumn leaves falling in Kyoto.",
    "Angela Merkel met Emmanuel Macron in Berlin to discuss the EU budget.",
    "Please translate 'good morning' into French, Spanish, and German.",
    "How many tablespoons are in a cup? Give the answer for US and UK measures.",
    "Dr. Smith's office (Room 4B) opens at 9:30 a.m. on weekdays.",
    "Email me at jane.doe@example.com or call +1-555-0100 before 5 p.m.",
    "Visit https://www.example.org/docs?page=2&lang=en#intro for details.",
    "Microsoft, Google and Amazon reported earnings on Thursday.",
    "José Martínez moved from São Paulo to Zürich in 2019.",
    "Explain the difference between TCP and UDP like I'm five.",
    "The quick brown fox jumps over the lazy dog.",
    "It's 72°F in Phoenix, Arizona — unusually cool for July.",
    "“Don’t panic,” said Arthur, clutching his towel.",
    "List three causes of the French Revolution and one consequence.",
    "Mount Kilimanjaro is the highest mountain in Africa at 5,895 m.",
    "Our Q3 revenue grew 12.5% year-over-year to $4.2B.",
    "Who painted the Mona Lisa, and where is it displayed today?",
    "Recommend a sci-fi novel similar to Dune by Frank Herbert.",
    "The meeting with Sundar Pichai is scheduled at the Googleplex in Mountain View.",
    "Convert 100 kilometres per hour to miles per hour.",
    "UNESCO designated the Great Barrier Reef a World Heritage Site in 1981.",
    "Is it safe to eat raw cookie dough? Answer briefly.",
    "Draft a polite reminder to Tom about the invoice due on 2024-03-15.",
    "Fill the <mask> here, then [MASK] and [CLS] text [SEP] <unk>"
    " <pad> \u2581already \u2581\u2581x",
)

CODE = (
    "def fib(n: int) -> int:\n    if n < 2:\n        re"
    "turn n\n    return fib(n - 1) + fib(n - 2)\n",
    'package main\n\nimport "fmt"\n\nfunc main() {\n\tfmt.Println("hello, world")\n}\n',
    "const add = (a, b) => a + b;\nconsole.log(add(2, 3)); // 5\n",
    "SELECT id, name, COUNT(*) AS n\nFROM users u\nJOIN orders o ON o.user_id = u.id\n"
    "WHERE u.created_at >= '2024-01-01'\nGROUP BY id, name\nHAVING n > 3;",
    '{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}], "stream": true}',
    "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: triage\nspec:\n  replicas: 3\n",
    '#!/usr/bin/env bash\nset -euo pipefail\nfor f in *.log; do gzip -9 "$f"; done\n',
    r"^(?P<user>[a-z0-9._%+-]+)@(?P<host>[a-z0-9.-]+\.[a-z]{2,})$",
    '<div class="card">\n  <h1>Title</h1>\n  <p>Body &amp; more</p>\n</div>',
    '#include <stdio.h>\n\nint main(void) {\n\tprintf("%d\\n", 42);\n\treturn 0;\n}\n',
    'fn main() {\n    let v: Vec<u32> = (1..=10).collect();\n    println!("{:?}", v);\n}\n',
    r"\begin{equation} E = mc^2 \quad \int_0^\infty e^{-"
    r"x^2}\,dx = \frac{\sqrt{\pi}}{2} \end{equation}",
    "| Name | Age |\n|------|-----|\n| Ann  | 31  |\n| Bob  | 27  |\n",
    '--- a/main.go\n+++ b/main.go\n@@ -1,3 +1,4 @@\n package main\n+import "os"\n',
    'Traceback (most recent call last):\n  File "app.py", line 12, in <module>\n'
    "    main()\nZeroDivisionError: division by zero",
    "x = [i ** 2 for i in range(10) if i % 2 == 0]  # squares of evens",
    "kubectl get pods -n kube-system -o jsonpath='{.items[*].metadata.name}'",
    "class Stack<T> {\n  private items: T[] = [];\n  push(x: T): void { this.items.push(x); }\n}",
    "UPDATE accounts SET balance = balance - 100 WHERE id = $1 RETURNING balance;",
    "if (a && b || !c) { return a != b ? a : b; } else { x <<= 2; y >>= 1; }",
    "curl -sS -X POST -H 'Content-Type: application/json' -d '{\"q\":1}' http://localhost:8080/v1",
    "<p>Price: <s>$20</s> now $15</p>\n<s>\n</s>  <mask>",
    "camelCaseIdentifier snake_case_identifier SCREAMING_CASE kebab-case-id",
    "0x7fffffff 0b1010 1e-9 3.14159 -0.0 NaN +Inf 1_000_000",
    "import numpy as np\narr = np.zeros((3, 4), dtype=np.float32)\nprint(arr.shape)",
)

KO = (
    "안녕하세요, 오늘 날씨가 정말 좋네요.",
    "서울에서 부산까지 KTX로 얼마나 걸리나요?",
    "이 문장을 영어로 번역해 주세요.",
    "김치찌개를 맛있게 끓이는 방법을 알려 주세요.",
    "대한민국의 수도는 서울입니다.",
    "파이썬으로 리스트를 정렬하는 코드를 작성해 줘.",
    "회의는 내일 오후 3시에 시작합니다.",
    "세종대왕은 1443년에 훈민정음을 창제했습니다.",
    "저는 학생이고, 컴퓨터 공학을 전공하고 있어요.",
    "가나다라마바사아자차카타파하",
    "인공지능이 일자리에 미치는 영향은 무엇인가요?",
    "제주도 여행 일정을 3박 4일로 짜 주세요.",
    "ㅋㅋㅋ 진짜 웃기다 ㅎㅎ",
    "이메일 주소는 hong@example.kr 입니다.",
    "비밀번호를 잊어버렸는데 어떻게 재설정하나요?",
    "한국어와 일본어의 문법 차이를 설명해 주세요.",
    "오늘 저녁에 뭐 먹을까? 치킨 아니면 피자?",
    "부산광역시 해운대구 우동 1234번지",
    "삼성전자와 LG전자의 2023년 매출을 비교해 줘.",
    "감사합니다! 좋은 하루 보내세요.",
)

VI = (
    "Xin chào, bạn có khỏe không?",
    "Hà Nội là thủ đô của Việt Nam.",
    "Hãy dịch câu này sang tiếng Anh giúp tôi.",
    "Phở bò Nam Định ngon nhất ở đâu?",
    "Tôi muốn đặt vé máy bay từ Thành phố Hồ Chí Minh đi Đà Nẵng.",
    "Giải thích thuật toán sắp xếp nhanh bằng tiếng Việt.",
    "Hôm nay trời mưa to quá, đường phố ngập nước.",
    "Nguyễn Du là tác giả của Truyện Kiều.",
    "Cảm ơn bạn rất nhiều vì đã giúp đỡ.",
    "Bao nhiêu tiền một ký xoài cát Hòa Lộc?",
    "Vịnh Hạ Long được UNESCO công nhận là di sản thiên nhiên thế giới.",
    "Tiếng Việt có sáu thanh điệu: ngang, huyền, sắc, hỏi, ngã, nặng.",
    "Làm thế nào để học lập trình Python hiệu quả?",
    "Cuộc họp sẽ bắt đầu lúc 9 giờ sáng thứ Hai.",
    "Ăn quả nhớ kẻ trồng cây.",
    "Địa chỉ: 123 Lê Lợi, Quận 1, TP.HCM",
    "Ngữ pháp tiếng Việt khác tiếng Trung như thế nào?",
    "Đừng quên mang ô khi ra ngoài nhé!",
    "Ước mơ của tôi là trở thành kỹ sư phần mềm.",
    "Quốc ngữ được viết bằng chữ Latinh với nhiều dấu phụ.",
)

ZH = (
    "你好，今天天气怎么样？",
    "请把这段话翻译成英文。",
    "北京是中华人民共和国的首都。",
    "如何用Python读取一个CSV文件？",
    "我想学习机器学习，应该从哪里开始？",
    "长城全长约两万一千公里。",
    "请写一首关于春天的五言绝句。",
    "上海浦东国际机场到市中心怎么走？",
    "人工智能会取代人类的工作吗？",
    "謝謝你的幫助，祝你有美好的一天。",
    "臺灣的總統府位於臺北市。",
    "这个价格是¥1,280元，含税。",
    "𠀀𠀁𪚥𰻞 are CJK Extension characters.",
)

JA = (
    "こんにちは、お元気ですか？",
    "東京から大阪まで新幹線でどのくらいかかりますか？",
    "この文章を英語に翻訳してください。",
    "日本の首都は東京です。",
    "カタカナとひらがなの違いを説明してください。",
    "ラーメンの美味しい作り方を教えて。",
    "富士山は日本で一番高い山です。",
    "明日の会議は午前10時から始まります。",
    "プログラミングを勉強しています。",
    "すみません、駅はどこですか？",
    "「吾輩は猫である」は夏目漱石の小説です。",
    "ｶﾞｷﾞｸﾞｹﾞｺﾞ\u3000半角カナ",
)

AR = (
    "مرحبا، كيف حالك اليوم؟",
    "ما هي عاصمة المملكة العربية السعودية؟",
    "من فضلك ترجم هذه الجملة إلى الإنجليزية.",
    "القاهرة هي أكبر مدينة في العالم العربي.",
    "أريد أن أتعلم البرمجة بلغة بايثون.",
    "شكرا\u064b جزيلا\u064b على مساعدتك.",
    "يبلغ عدد سكان مصر أكثر من ١٠٠ مليون نسمة.",
    "الذكاء الاصطناعي يغير طريقة عملنا.",
    "كتب نجيب محفوظ الثلاثية.",
    "م\u064fح\u064eم\u064e\u0651د\u064c ر\u064eس\u064fول\u064f الل\u064e\u0651ه\u0650",
    "الس\u064e\u0651ل\u064eام\u064f ع\u064eل\u064eي\u0652ك\u064fم\u0652"
    " و\u064eر\u064eح\u0652م\u064eة\u064f الل\u064e\u0651ه\u0650",
    "ﷺ ﷲ ﻻ ﻷ",
    "سعر الدولار اليوم ٣٫٧٥ ريال.",
    "اكتب لي قصيدة قصيرة عن البحر.",
    "Hello مرحبا 123 ١٢٣",
)

EMOJI = (
    ("Great job! 👍", "en"),
    ("Thumbs up 👍🏻👍🏼👍🏽👍🏾👍🏿", "en"),
    ("Family: 👨\u200d👩\u200d👧\u200d👦", "en"),
    ("Rainbow flag 🏳\ufe0f\u200d🌈 and pirate flag 🏴\u200d☠\ufe0f", "en"),
    ("Developer 👩🏽\u200d💻 and scientist 🧑🏿\u200d🔬", "en"),
    ("Flags: 🇻🇳 🇰🇷 🇯🇵 🇺🇸 🇩🇪", "en"),
    ("Keycaps 1\ufe0f\u20e3 2\ufe0f\u20e3 #\ufe0f\u20e3 *\ufe0f\u20e3", "en"),
    ("Scotland 🏴\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074\U000e007f", "en"),
    ("I ❤\ufe0f you, not ❤\ufe0e text-style", "en"),
    ("😂😂😂😂😂", "en"),
    ("🫨\U0001fae9🪿🫎 new emoji", "en"),
    ("Couple 👩\u200d❤\ufe0f\u200d💋\u200d👨 kissing", "en"),
    ("Handshake 🫱🏻\u200d🫲🏿", "en"),
    ("Lone modifiers 🏻🏼🏽", "en"),
    ("Regional indicators without pair: 🇦 🇧", "en"),
    ("Woman shrugging 🤷\u200d♀\ufe0f man facepalming 🤦🏼\u200d♂\ufe0f", "en"),
    ("ZWJ broken: 👨\u200d\u200d👩", "en"),
    ("Ok 👌 cool 😎 fire 🔥 100 💯", "en"),
    ("Weather: ☀\ufe0f ⛅ 🌧\ufe0f ❄\ufe0f ⚡", "en"),
    ("Math-ish ➕➖✖\ufe0f➗", "en"),
    ("오늘 기분 최고 😄🎉", "ko"),
    ("Chúc mừng năm mới 🧧🎆", "vi"),
    ("生日快乐🎂🎁", "zh"),
    ("おはよう☀\ufe0f🍙", "ja"),
    ("مبروك 🎉🎊", "ar"),
    ("Food 🍕🍔🍟🌮🍣🍜", "en"),
    ("Hearts 💙💚💛🧡💜🖤🤍🤎", "en"),
    ("Animals 🐶🐱🐭🐹🐰🦊🐻\u200d❄\ufe0f", "en"),
    ("🧑\u200d🧑\u200d🧒\u200d🧒", "en"),
    ("Arrows ↔\ufe0f ↕\ufe0f ⬆\ufe0f ➡\ufe0f and symbols © ® ™", "en"),
)

ZERO_WIDTH_CHARS = (
    ("\u200d", "zwj"),
    ("\u200b", "zwsp"),
    ("\ufeff", "bom"),
    ("\u200c", "zwnj"),
    ("\u2060", "wj"),
    ("\u00ad", "shy"),
    ("\u200e", "lrm"),
)

# (base text, lang): zero-width characters are inserted at fixed positions.
ZERO_WIDTH_BASES = (
    ("Hello world", "en"),
    ("John Smith lives in Boston", "en"),
    ("안녕하세요 세계", "ko"),
    ("Xin chào thế giới", "vi"),
    ("你好世界", "zh"),
)

NFKC = (
    ("Ｈｅｌｌｏ\u3000Ｗｏｒｌｄ！", "en"),
    ("Full-width digits ０１２３４５６７８９ and ＡＢＣ", "en"),
    ("Ligatures: ﬁnance ﬂow eﬀort oﬃce ﬄuﬀy", "en"),
    ("Superscripts x² y³ and subscripts H₂O CO₂", "en"),
    ("Fractions ½ ¼ ¾ ⅓ ⅞", "en"),
    ("Roman numerals Ⅰ Ⅱ Ⅻ ⅷ ⅿ", "en"),
    ("Circled ① ② ⑳ Ⓐ ⓩ", "en"),
    ("Units ㎏ ㎞ ㎖ ㏄ ㍿ ㈱", "en"),
    ("Symbols ℃ ℉ № ™ ℡ ℅", "en"),
    ("Angstrom Å vs Å, ohm Ω vs Ω, kelvin K vs K", "en"),
    ("Decomposed cafe\u0301 vs precomposed café", "en"),
    ("Na\u0308ive coo\u0308perate re\u0301sume\u0301", "en"),
    ("Stacked a\u0301\u0302\u0303\u0304 marks", "en"),
    ("Many marks e\u0301\u0301\u0301\u0301\u0301\u0301 end", "en"),
    ("Zalgo h\u0336\u0335e\u0338\u0337l\u0334\u0335l\u0336o\u0337", "en"),
    ("Math bold \U0001d407\U0001d41e\U0001d425\U0001d425\U0001d428", "en"),
    ("Math italic \U0001d44e\U0001d44f\U0001d450 and double-struck ℝ ℕ ℤ", "en"),
    ("Long s ſtraße, sharp S ẞ, micro µ vs mu μ", "en"),
    ("Ellipsis … two-dot ‥ double ‼ interrobang ⁉", "en"),
    ("Enclosed \U0001f130\U0001f131 \U0001f170 squared \U0001f1a5", "en"),
    ("Digraphs Ǆ ǅ ǆ Ǉ Ǳ Ĳ ĳ", "en"),
    ("Spacing modifiers ʰ ˠ and ¨ ´ ¸", "en"),
    ("Dashes ‐ ‑ ‒ – — ― minus −", "en"),
    ("Primes ′ ″ ‴ and quotes „ ‟ ‹ ›", "en"),
    ("Small forms ﹐ ﹒ ﹖ ﹗ ﹙", "en"),
    ("Half-width ｱｲｳｴｵ ﾊﾟﾋﾟﾌﾟ ﾞﾟ", "ja"),
    ("Hangul jamo 한글", "ko"),
    ("Compatibility jamo ㄱㄴㄷㄹ ㅏㅑㅓ", "ko"),
    ("Halfwidth hangul ﾡﾢﾣ ￂￃ", "ko"),
    ("Vietnamese decomposed Vie\u0323\u0302t Nam", "vi"),
    ("Tie\u0302\u0301ng Vie\u0323\u0302t co\u0301 da\u0302\u0301u", "vi"),
    ("CJK compatibility 豈 更 侮 and radicals ⼀ ⼈", "zh"),
    ("Full-width punctuation ，。！？：；（）", "zh"),
    ("Arabic presentation ﺍﺎﺏ ﻻﻼ", "ar"),
    ("Arabic ligature ﷺ and ﷻ", "ar"),
    ("Parenthesized ⑴ ⒜ ㈀ ㈠", "en"),
    ("Square words ㌀ ㌍ ㍻ ㍿", "ja"),
    ("Greek tonos ΐ ά vs oxia ά Ά", "en"),
    ("Hebrew ligature ﭏ and points ש\u05c1\u05bc", "en"),
    ("Kelvin K, ohm Ω, and ⅠⅡⅢ in one line: ＡＢＣ", "en"),
)

WHITESPACE = (
    ("Hello    world", "en"),
    ("Tabs\tbetween\t\twords", "en"),
    ("Line one\nLine two\n\nLine four", "en"),
    ("Windows\r\nline\r\nendings", "en"),
    ("   leading spaces", "en"),
    ("trailing spaces   ", "en"),
    ("   both sides   ", "en"),
    ("", "en"),
    (" ", "en"),
    ("\n\n\n", "en"),
    ("\t", "en"),
    ("No-break\u00a0space and narrow\u202fno-break", "en"),
    ("En\u2002em\u2003thin\u2009hair\u200asix\u2006per\u2005em", "en"),
    ("Ideographic\u3000space", "en"),
    ("Line\u2028separator and paragraph\u2029separator", "en"),
    ("Next\u0085line and ogham\u1680space", "en"),
    ("Medium\u205fmath space", "en"),
    ("Vertical\u000btab and form\u000cfeed", "en"),
    ("Mixed \t \n \r \u00a0 \u3000 run", "en"),
    ("word" + " " * 40 + "word", "en"),
    ("a\tb\tc\n1\t2\t3\n", "en"),
    ("Spaces before punctuation , . ! ?", "en"),
    ("한국어\t탭\n줄바꿈", "ko"),
    ("Tiếng   Việt\u00a0\u00a0khoảng", "vi"),
    ("中文\u3000全角空格\n换行", "zh"),
    ("Control\u0007bell\u001b[31mred\u001b[0m", "en"),
    ("Null\u0000byte and delete\u007f", "en"),
    ("Private use \ue000\uf8ff and replacement �", "en"),
    ("C1 controls \u0080\u0081\u009f", "en"),
    ("    ", "en"),
)

MIXED = (
    ("Größe, Öl und Übermaß in München", "de"),
    ("L'œuvre de Molière est très célèbre à Paris", "fr"),
    ("¿Qué hora es? ¡Vamos a la playa!", "es"),
    ("Привет, как дела? Москва — столица России.", "ru"),
    ("Καλημέρα κόσμε, η Αθήνα είναι όμορφη.", "el"),
    ("שלום עולם, ירושלים", "he"),
    ("สว\u0e31สด\u0e35คร\u0e31บ ว\u0e31นน\u0e35\u0e49อากาศด\u0e35มาก", "th"),
    ("नमस\u094dत\u0947, आप क\u0948स\u0947 ह\u0948\u0902? क\u094dषत\u094dरिय श\u094dरी", "hi"),
    ("Tôi thích học English và 한국어", "vi"),
    ("이 코드에서 for loop가 왜 안 돼요? print(x)", "ko"),
    ("请用 Python 写一个 hello world 程序", "zh"),
    ("東京 Tokyo 서울 Seoul Hà Nội", "mul"),
    ("Ελληνικά, Русский, العربية, 日本語, 한국어", "mul"),
    ("বাংলা ভাষা, தமிழ\u0bcd, ગ\u0ac1જરાતી", "mul"),
    ("Ğüzel İstanbul'da ışık", "tr"),
)

LONG_PARAGRAPHS = (
    (
        "The history of computing is a story of abstraction layered upon abstraction. "
        "Early engineers wired programs by hand, later generations wrote assembly, and "
        "today most developers rarely think about the instructions their code becomes. "
        "Each layer hides complexity, but every layer also leaks a little, and the best "
        "engineers learn to look beneath the surface when performance or correctness "
        "demands it. ",
        "en",
    ),
    (
        "인공지능 기술은 최근 몇 년 동안 빠르게 발전해 왔습니다. 특히 대규모 언어 모델은 "
        "번역, 요약, 질문 답변과 같은 다양한 작업에서 놀라운 성능을 보여 주고 있습니다. "
        "하지만 이러한 모델은 많은 계산 자원을 필요로 하며, 편향과 환각 문제도 여전히 "
        "해결해야 할 과제로 남아 있습니다. ",
        "ko",
    ),
    (
        "Việt Nam là một quốc gia nằm ở phía đông bán đảo Đông Dương. Với bờ biển dài hơn "
        "ba nghìn cây số, đất nước này có nhiều bãi biển đẹp và vịnh nổi tiếng. Ẩm thực "
        "Việt Nam được biết đến với phở, bánh mì và nhiều món ăn đường phố hấp dẫn. ",
        "vi",
    ),
    (
        "机器学习是人工智能的一个分支，它使计算机能够从数据中学习，而无需进行明确的编程。"
        "近年来，深度学习在图像识别、语音识别和自然语言处理等领域取得了显著的进展。",
        "zh",
    ),
    (
        "日本の四季はそれぞれ異なる魅力を持っています。春には桜が咲き、夏には祭りが"
        "行われ、秋には紅葉が美しく、冬には雪景色を楽しむことができます。",
        "ja",
    ),
    (
        "تعتبر اللغة العربية من أقدم اللغات في العالم، ويتحدث بها أكثر من أربعمائة مليون "
        "شخص. وهي اللغة الرسمية في أكثر من عشرين دولة، كما أنها إحدى اللغات الرسمية الست "
        "في الأمم المتحدة. ",
        "ar",
    ),
    (
        "def process(items):\n    result = []\n    for item in items:\n        if item.valid:\n"
        "            result.append(transform(item))\n    return result\n\n",
        "en",
    ),
    (
        "Emoji 😀 mixed 👨\u200d👩\u200d👧 with text\u200b and zero\u200dwidth\ufeff "
        "chars, ｆｕｌｌ-width, ﬁ ligatures, and cafe\u0301 accents. ",
        "en",
    ),
)

# (paragraph index, repetitions): each result is well over 512 XLM-R tokens.
LONG_SPECS = (
    (0, 7),
    (0, 20),
    (1, 9),
    (2, 10),
    (3, 14),
    (4, 16),
    (5, 15),
    (6, 18),
    (7, 19),
)


def _numbered(prefix: str, items: Iterable[tuple[str, str, str]]) -> list[Prompt]:
    return [
        Prompt(id=f"{prefix}-{i:03d}", category=cat, text=text, lang=lang)
        for i, (cat, text, lang) in enumerate(items, start=1)
    ]


def _zero_width() -> list[tuple[str, str, str]]:
    """35 texts: every character at the start, inside a word, between words and doubled."""
    out: list[tuple[str, str, str]] = []
    for ch, _name in ZERO_WIDTH_CHARS:
        for j, (base, lang) in enumerate(ZERO_WIDTH_BASES):
            mid = len(base) // 2
            variants = (
                ch + base,
                base[:mid] + ch + base[mid:],
                base.replace(" ", " " + ch, 1),
                base.replace(" ", ch + ch, 1) + ch,
                base[:2] + ch + base[2:4] + ch + base[4:],
            )
            out.append(("zero_width", variants[j], lang))
    return out


def _long() -> list[tuple[str, str, str]]:
    out: list[tuple[str, str, str]] = []
    for idx, reps in LONG_SPECS:
        para, lang = LONG_PARAGRAPHS[idx]
        out.append(("long", para * reps, lang))
    # Mixed-language long text and pathological single "words".
    mixed = "".join(p for p, _ in LONG_PARAGRAPHS) * 3
    out.append(("long", mixed, "mul"))
    out.append(("long", "a" * 3000, "en"))
    out.append(("long", "ab1-" * 700, "en"))
    out.append(("long", "QmFzZTY0IGVuY29kZWQgZGF0YQ==" * 80, "en"))
    out.append(("long", "가나다라마바사" * 300, "ko"))
    out.append(("long", " ".join(f"word{i}" for i in range(700)), "en"))
    return out


def build_corpus() -> list[Prompt]:
    """Return the 300-prompt tokenizer corpus in its committed order."""
    groups: list[tuple[str, list[tuple[str, str, str]]]] = [
        ("en", [("en", t, "en") for t in EN]),
        ("code", [("code", t, "en") for t in CODE]),
        ("ko", [("ko", t, "ko") for t in KO]),
        ("vi", [("vi", t, "vi") for t in VI]),
        ("cjk", [("cjk", t, "zh") for t in ZH] + [("cjk", t, "ja") for t in JA]),
        ("ar", [("ar", t, "ar") for t in AR]),
        ("emoji", [("emoji", t, lang) for t, lang in EMOJI]),
        ("zw", _zero_width()),
        ("nfkc", [("nfkc", t, lang) for t, lang in NFKC]),
        ("ws", [("whitespace", t, lang) for t, lang in WHITESPACE]),
        ("mixed", [("mixed", t, lang) for t, lang in MIXED]),
        ("long", _long()),
    ]
    corpus: list[Prompt] = []
    for prefix, items in groups:
        corpus.extend(_numbered(prefix, items))
    if len(corpus) != CORPUS_SIZE:
        msg = f"corpus has {len(corpus)} prompts, want {CORPUS_SIZE}"
        raise ValueError(msg)
    return corpus


_INVISIBLE = frozenset({"Cc", "Cf", "Co", "Cs", "Zl", "Zp", "Zs"})


def _escape_char(ch: str) -> str:
    if ch == " " or unicodedata.category(ch) not in _INVISIBLE:
        return ch
    code = ord(ch)
    if code <= _MAX_R16:
        return f"\\u{code:04x}"
    code -= 0x10000
    return f"\\u{0xD800 + (code >> 10):04x}\\u{0xDC00 + (code & 0x3FF):04x}"


def _dump(obj: dict[str, object]) -> str:
    """JSON with readable scripts but invisible and separator characters escaped."""
    raw = json.dumps(obj, ensure_ascii=False, separators=(",", ":"))
    return "".join(_escape_char(ch) for ch in raw)


def write_corpus(path: Path, corpus: Sequence[Prompt]) -> None:
    """Write the corpus as JSON lines ``{"id","category","text","lang"}``."""
    lines = [
        _dump({"id": p.id, "category": p.category, "text": p.text, "lang": p.lang}) for p in corpus
    ]
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")


def read_corpus(path: Path) -> list[Prompt]:
    """Read a corpus written by :func:`write_corpus`."""
    out: list[Prompt] = []
    for line in path.read_text(encoding="utf-8").split("\n"):
        if not line:
            continue
        row = json.loads(line)
        out.append(
            Prompt(id=row["id"], category=row["category"], text=row["text"], lang=row["lang"])
        )
    return out


def load_tokenizer(path: Path) -> Tokenizer:
    """Load a ``tokenizer.json`` with truncation and padding disabled."""
    tok = Tokenizer.from_file(str(path))
    tok.no_truncation()
    tok.no_padding()
    return tok


def encode_all(tok: Tokenizer, corpus: Sequence[Prompt]) -> list[dict[str, object]]:
    """Encode each prompt with special tokens as the model uses them."""
    return [{"id": p.id, "ids": tok.encode(p.text, add_special_tokens=True).ids} for p in corpus]


def write_reference(path: Path, rows: Sequence[dict[str, object]]) -> None:
    """Write reference rows ``{"id","ids"}`` as JSON lines."""
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("".join(_dump(r) + "\n" for r in rows), encoding="utf-8")


def _check_long(corpus: Sequence[Prompt], rows: Sequence[dict[str, object]]) -> None:
    by_id = {str(r["id"]): r["ids"] for r in rows}
    for p in corpus:
        ids = by_id[p.id]
        if p.category == "long" and isinstance(ids, list) and len(ids) < LONG_MIN_IDS:
            msg = f"{p.id}: {len(ids)} ids, long prompts need >= {LONG_MIN_IDS}"
            raise ValueError(msg)


def run_reference(corpus_path: Path, tokenizer_dir: Path, out_dir: Path) -> tuple[int, int]:
    """Write ``reference_bert.jsonl`` and ``reference_ner.jsonl``; return their line counts."""
    corpus = read_corpus(corpus_path)
    bert = encode_all(load_tokenizer(tokenizer_dir / "bert" / "tokenizer.json"), corpus)
    _check_long(corpus, bert)
    english = [p for p in corpus if p.lang == NER_LANG]
    ner = encode_all(load_tokenizer(tokenizer_dir / "ner" / "tokenizer.json"), english)
    write_reference(out_dir / "reference_bert.jsonl", bert)
    write_reference(out_dir / "reference_ner.jsonl", ner)
    return len(bert), len(ner)


def _ranges(cps: Iterable[int]) -> list[tuple[int, int]]:
    out: list[tuple[int, int]] = []
    for cp in sorted(cps):
        if out and out[-1][1] == cp - 1:
            out[-1] = (out[-1][0], cp)
        else:
            out.append((cp, cp))
    return out


def bert_char_classes(tok: Tokenizer) -> tuple[list[int], list[int]]:
    """Probe every code point: (removed by BertNormalizer, isolated by BertPreTokenizer).

    HF ``tokenizers`` takes these classes from the ``unicode_categories`` crate, whose tables
    predate the Unicode version of Go's ``unicode`` package, so Go embeds the probed sets.
    """
    if tok.normalizer is None or tok.pre_tokenizer is None:
        msg = "tokenizer has no normalizer or pre-tokenizer"
        raise ValueError(msg)
    removed: list[int] = []
    punct: list[int] = []
    for cp in range(0x110000):
        if _SURROGATES[0] <= cp <= _SURROGATES[1]:
            continue
        ch = chr(cp)
        if tok.normalizer.normalize_str(ch) == "":
            removed.append(cp)
        pieces = tok.pre_tokenizer.pre_tokenize_str("a" + ch + "a")
        if len(pieces) == _ISOLATED_PIECES and pieces[1][0] == ch:
            punct.append(cp)
    return removed, punct


def _go_table(name: str, doc: str, cps: Sequence[int]) -> str:
    r16 = [r for r in _ranges(cps) if r[1] <= _MAX_R16]
    r32 = [r for r in _ranges(cps) if r[0] > _MAX_R16]
    if any(lo <= _MAX_R16 < hi for lo, hi in _ranges(cps)):
        msg = f"{name}: range crosses U+FFFF"
        raise ValueError(msg)
    lines = [f"// {doc}", f"var {name} = &unicode.RangeTable{{", "\tR16: []unicode.Range16{"]
    lines += [f"\t\t{{Lo: 0x{lo:04x}, Hi: 0x{hi:04x}, Stride: 1}}," for lo, hi in r16]
    lines += ["\t},", "\tR32: []unicode.Range32{"]
    lines += [f"\t\t{{Lo: 0x{lo:x}, Hi: 0x{hi:x}, Stride: 1}}," for lo, hi in r32]
    lines += ["\t},", "}", ""]
    return "\n".join(lines)


def write_go_tables(path: Path, removed: Sequence[int], punct: Sequence[int]) -> None:
    """Write the probed BertNormalizer/BertPreTokenizer classes as Go range tables."""
    header = (
        "// SPDX-License-Identifier: Apache-2.0\n\n"
        "// Code generated by research/src/triage_research/reference_tokenize.py tables; "
        "DO NOT EDIT.\n\n"
        'package hftok\n\nimport "unicode"\n\n'
    )
    body = (
        _go_table(
            "bertRemoved",
            "bertRemoved holds the code points BertNormalizer(clean_text) deletes.",
            removed,
        )
        + "\n"
        + _go_table(
            "bertPunct",
            "bertPunct holds the code points BertPreTokenizer isolates as punctuation.",
            punct,
        )
    )
    path.write_text(header + body, encoding="utf-8")


def main(argv: list[str] | None = None) -> int:
    """CLI: ``python -m triage_research.reference_tokenize corpus|reference|tables``."""
    parser = argparse.ArgumentParser(prog="triage-reference-tokenize", description=__doc__)
    parser.add_argument("command", choices=("corpus", "reference", "tables"))
    parser.add_argument("--go-tables", type=Path, default=DEFAULT_GO_TABLES)
    parser.add_argument("--out-dir", type=Path, default=DEFAULT_OUT_DIR)
    parser.add_argument(
        "--tokenizer-dir",
        type=Path,
        default=Path(os.environ.get("TRIAGE_TOKENIZER_DIR", str(DEFAULT_TOKENIZER_DIR))),
    )
    args = parser.parse_args(argv)
    if args.command == "corpus":
        write_corpus(args.out_dir / CORPUS_FILE, build_corpus())
        sys.stdout.write(f"wrote {args.out_dir / CORPUS_FILE}\n")
        return 0
    tokenizer_dir = args.tokenizer_dir.expanduser()
    if args.command == "tables":
        removed, punct = bert_char_classes(load_tokenizer(tokenizer_dir / "ner" / "tokenizer.json"))
        write_go_tables(args.go_tables, removed, punct)
        sys.stdout.write(f"wrote {args.go_tables} ({len(removed)} removed, {len(punct)} punct)\n")
        return 0
    n_bert, n_ner = run_reference(args.out_dir / CORPUS_FILE, tokenizer_dir, args.out_dir)
    sys.stdout.write(f"wrote reference_bert.jsonl ({n_bert}) and reference_ner.jsonl ({n_ner})\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
