#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

DATA_DIR="${EVAL_DATA_DIR:-$HOME/.cache/rayld/triage/eval/data}"
GSM8K_REV=740312add88f781978c0658806c59bc2815b9866
MMLU_REV=c30699e8356da336a370243923dbaf21066bb9fe
ROUTELLM_REV=0b64fdafe049e596a3f5657c219329f24af24198

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'; else shasum -a 256 "$1" | awk '{print $1}'; fi
}

url_of() {
	case "$1" in
	hf-gsm8k/*) echo "https://huggingface.co/datasets/openai/gsm8k/resolve/$GSM8K_REV/${1#hf-gsm8k/}" ;;
	hf-mmlu/*) echo "https://huggingface.co/datasets/cais/mmlu/resolve/$MMLU_REV/${1#hf-mmlu/}" ;;
	routellm/*) echo "https://raw.githubusercontent.com/lm-sys/RouteLLM/$ROUTELLM_REV/routellm/evals/${1#routellm/}" ;;
	*) echo "fetch_eval_data: unknown source in $1" >&2; return 1 ;;
	esac
}

fetch() {
	local rel="$1" want="$2" dest tmp got
	dest="$DATA_DIR/raw/$rel"
	if [ -f "$dest" ] && [ "$(sha256_of "$dest")" = "$want" ]; then
		echo "cached   $rel"
		return 0
	fi
	mkdir -p "$(dirname "$dest")"
	tmp="$(mktemp "$dest.XXXXXX")"
	if ! curl -fsSL --retry 3 -o "$tmp" "$(url_of "$rel")"; then
		rm -f "$tmp"
		echo "fetch_eval_data: download failed: $rel" >&2
		return 1
	fi
	got="$(sha256_of "$tmp")"
	if [ "$got" != "$want" ]; then
		rm -f "$tmp"
		echo "fetch_eval_data: SHA-256 mismatch for $rel: got $got, want $want" >&2
		return 1
	fi
	mv "$tmp" "$dest"
	echo "fetched  $rel"
}

manifest() {
	cat <<'MANIFEST'
ee7b8da9e381df27b9e3f7758a159ab2bdaa4dbaa910546cbbc47e0cb44e4f59  hf-gsm8k/main/test-00000-of-00001.parquet
92338cdcdb43d2871691ef9a0e125e633ca57335ed635a635fe6dead94f224c2  hf-mmlu/abstract_algebra/test-00000-of-00001.parquet
cb88f5920b67ab290dd23a825f8e3826d01d8f48d99ad4931a8b0003a952a211  hf-mmlu/anatomy/test-00000-of-00001.parquet
bcdfe1afcb9421747b4edd8d426ac87d47fd4d03e7c3455bcb149d1ba14db228  hf-mmlu/astronomy/test-00000-of-00001.parquet
7910a4b8236c64ef339410a41d5806637087c84635278dda0ff674b8f894dfbb  hf-mmlu/business_ethics/test-00000-of-00001.parquet
7bd3de302db35c6e9adb311ef40b2fb15ffe02ada17667119d9fb30fe393a44e  hf-mmlu/clinical_knowledge/test-00000-of-00001.parquet
5b6920ddbbb40ee6efd1e6c8814be9fd03d24c9e1e1dbd4b02325e2d454a6a82  hf-mmlu/college_biology/test-00000-of-00001.parquet
f012f90fc3aec5ead6963fe7bdeee58783010265538dda3be984a9d1dbbd7051  hf-mmlu/college_chemistry/test-00000-of-00001.parquet
08b79916057e75938a2a0d5ceb10c517cf923580e6058af48b1b7b3f7a52c5b8  hf-mmlu/college_computer_science/test-00000-of-00001.parquet
f57b57e992d03d07a05c6f8f328a14a49870f61d91e8e525a062644e234bf4f0  hf-mmlu/college_mathematics/test-00000-of-00001.parquet
f21b3166975dbc14dff5940df41479bc28853008303c50a070d430fc59468f9c  hf-mmlu/college_medicine/test-00000-of-00001.parquet
81b8eafc776b3109fd0ba9f527a7f759b07b92fe953743d3711086e5208aadca  hf-mmlu/college_physics/test-00000-of-00001.parquet
a6ad5774acf5133ac88cf1bb066c60db8668abee2d6a724cf2f9dfa7cb4f103d  hf-mmlu/computer_security/test-00000-of-00001.parquet
c66a861048a25f96baa59517b613c3ed1a26abf4d7717ae5aab2da29bac7fa70  hf-mmlu/conceptual_physics/test-00000-of-00001.parquet
63501e9de9f822b4d0f60919024c660b40a78791b9cfcf1e69e759f16eef9123  hf-mmlu/econometrics/test-00000-of-00001.parquet
3df176cabbd13a1315d7312f37ffec139f1cf51fc4ea3407b5293a00ac325570  hf-mmlu/electrical_engineering/test-00000-of-00001.parquet
9cb645300084d3b7ff8af98efef034068f8c8780f06017e2ebf21d33beb2fc61  hf-mmlu/elementary_mathematics/test-00000-of-00001.parquet
a9f917e01461be6ef71bf32248bf10c3a0580d2fef641255b265131bf29d876c  hf-mmlu/formal_logic/test-00000-of-00001.parquet
9e4b5a22ad1cbe5c58c21f6a96fe65bd0dbb05c8295e1db6fc57c9ee7676b6ef  hf-mmlu/global_facts/test-00000-of-00001.parquet
73600cc5c8032ffd20115887ec4f2c868d75c403c70ac1d115295e72cb8d80e3  hf-mmlu/high_school_biology/test-00000-of-00001.parquet
fde2345147be31d8df06ad66200521d7e47588490ab59c09fd78861c8a373ccb  hf-mmlu/high_school_chemistry/test-00000-of-00001.parquet
6e1b210896afc60f2ea3a1b011733b86dedd74690adce6ff0893346089f83252  hf-mmlu/high_school_computer_science/test-00000-of-00001.parquet
2bcf46d76656082263b22d871c6ba16f29f4abed600d07381018b843769620c1  hf-mmlu/high_school_european_history/test-00000-of-00001.parquet
d083f0a37e9802ff70af8c0ccee95dc6836d80db74f45327519756327076a2c1  hf-mmlu/high_school_geography/test-00000-of-00001.parquet
54bb3fcc3987afc3ce0e605343752de94ca227bd4ae13c84b7b1ec9bc3998292  hf-mmlu/high_school_government_and_politics/test-00000-of-00001.parquet
a0f2d6a33352452442a13ffb947ab04dcf16513e87bee7c52e8957c2bc761a4c  hf-mmlu/high_school_macroeconomics/test-00000-of-00001.parquet
310628511fac990528ecac65634fa1ab924ce2d4aad7a7d9cded06db17f6cf7d  hf-mmlu/high_school_mathematics/test-00000-of-00001.parquet
8a55dc2fa10fa86cc4cf5565bb49416bdcf45b545ebb83f6ce8e4360cc65b453  hf-mmlu/high_school_microeconomics/test-00000-of-00001.parquet
48fc84060e4cd032a407da09164e99dfdf81f3ce345bf6c1871bb539ba4ead6c  hf-mmlu/high_school_physics/test-00000-of-00001.parquet
9df4a744eb6faa12bb359df1f808c0461c055f240b998a9ec9d97b1c4e49a958  hf-mmlu/high_school_psychology/test-00000-of-00001.parquet
4922828bda3ae4c4470e95d8fa809e3abe18f6239047b21d675267ebefb1c691  hf-mmlu/high_school_statistics/test-00000-of-00001.parquet
7e3c242fa3b087b918d30a5202948ed462363ca9de9e65a76970e2c58d5659b7  hf-mmlu/high_school_us_history/test-00000-of-00001.parquet
abc9b0182e7cdd19ef7b70ec9734f62436f99e76c740c67f41155611eccbcd7d  hf-mmlu/high_school_world_history/test-00000-of-00001.parquet
69b143d394fd115c893afd98ed8fddaf32039bdece6a60c7f0e6caa3e2a97847  hf-mmlu/human_aging/test-00000-of-00001.parquet
49efe58e13116a40ac9ee640da8b9c41b29a2915c86eb5bb33fb2959d0c724f9  hf-mmlu/human_sexuality/test-00000-of-00001.parquet
05da704a004fcc5c7d3d47a894af201ca26d8e071aadf05e3a34155fad968ccf  hf-mmlu/international_law/test-00000-of-00001.parquet
0f302db5178cb629b3b1514cc5f9b2a94e6bbbf0925e3d18b09ed4394099e059  hf-mmlu/jurisprudence/test-00000-of-00001.parquet
87eefcf75ec88f69379d848bddab3f8936e4d340ef1e51498c334c5a76031fe7  hf-mmlu/logical_fallacies/test-00000-of-00001.parquet
cd3d1ac01f117433bfed153f81425ede65b1d57abd7bebd2a3471b7b22334424  hf-mmlu/machine_learning/test-00000-of-00001.parquet
b5579485f3b83ceabcf2269720c7ce087b76ad473f020336c5ec7e724900767f  hf-mmlu/management/test-00000-of-00001.parquet
3c975f1b0dc699b113fe9ac9a438cda04995b5fab8613e949693cd85e360240a  hf-mmlu/marketing/test-00000-of-00001.parquet
f5535475f3afc3479697355679f2b615603fe90a58e8e5b6a98e50ccf07171b9  hf-mmlu/medical_genetics/test-00000-of-00001.parquet
5665dde94d34f04a0310bb2af2f8a27fc09a8a2629fe1e6e9248b11592dbd060  hf-mmlu/miscellaneous/test-00000-of-00001.parquet
fc2a4270481cf732dcbce6816231b430bec14e327e28db930b396e7e53c6c775  hf-mmlu/moral_disputes/test-00000-of-00001.parquet
e21cc9d8f5a1898bd4bb080fa5dad2474f67f27ca98d3c7bb8c7f529ea32a9f4  hf-mmlu/moral_scenarios/test-00000-of-00001.parquet
7ba291c21774407b1b27349172c3f85211af81df5047fe92b83b961d7bb7f03f  hf-mmlu/nutrition/test-00000-of-00001.parquet
51a6715bca767d89652f782825610b7acd22dd5feb9331dce53cb52a772e4d22  hf-mmlu/philosophy/test-00000-of-00001.parquet
f4cf1e103e6494dd2f77bd1d732b58d8386cc3182346609b7c04353207122a25  hf-mmlu/prehistory/test-00000-of-00001.parquet
1fe6da7a3a8647ee70c23543a15ef9b3ad5bf9ec22e68d0583c98dfdf7585e0f  hf-mmlu/professional_accounting/test-00000-of-00001.parquet
81cfdb63639fe7839da36aa1156e3f772a4521d532027d2c56f1db8fb30c6c7c  hf-mmlu/professional_law/test-00000-of-00001.parquet
8c1c18b16c9865bf52b7cd997c4d05346f914441645459fac8c5b2f78d9ebac9  hf-mmlu/professional_medicine/test-00000-of-00001.parquet
91529edc6760dbe5bfa41e9190021041499a15f332f5af8d9bffc9fb252116b9  hf-mmlu/professional_psychology/test-00000-of-00001.parquet
9ce4ff437b2b6192505d2b5dbe745a5012711ce911ada34f8574fb18411d07cd  hf-mmlu/public_relations/test-00000-of-00001.parquet
38477f527d22bc9f55e92fb7c247f73e301508d547841b591bd0f9f0fc9529d5  hf-mmlu/security_studies/test-00000-of-00001.parquet
0cdb1dab92e7d5a8c1b617b1c868ae72dade770eb64d6539b26e256594a5d67a  hf-mmlu/sociology/test-00000-of-00001.parquet
8051dfc5f49bc73c4882049cbfe476fea2f57ec1879eb23319ed4bdd8f6b0cd3  hf-mmlu/us_foreign_policy/test-00000-of-00001.parquet
c59ea23f72b405b180a3135c3a5240f8594af6f3c03e5c7784345914253a4928  hf-mmlu/virology/test-00000-of-00001.parquet
b59bdb0cb4b16fd8a0a81362d8bd8241c0013940b877c5c6e76d66f536613aff  hf-mmlu/world_religions/test-00000-of-00001.parquet
1f5a7be02148ee0f7f306d233ba76a6573c826cee9499a34f8ef938a1a84c234  routellm/gsm8k/contaminated_prompts.jsonl
1f38ed03db354a020f71e92022383dc3cf37bbf2694dea4daaeea7b6c43fe57b  routellm/gsm8k/gsm8k_responses.csv
9606c968198eabf5efa46ccba31286392c8e35525d83c86661618ffc5ec6d40e  routellm/mmlu/contaminated_prompts.jsonl
67e564eea42e71fbfcaf616972df952c316e5538376da1c16d691177458b96b9  routellm/mmlu/responses/mmlu_abstract_algebra.csv
53ebea8257805eeecc25713728264b8235cffad7b720962a3eafc92fd27ef537  routellm/mmlu/responses/mmlu_anatomy.csv
49915ece9dfe8aadbd351f221ada2ea86b6f832ce49fa64d1622a0f066c19794  routellm/mmlu/responses/mmlu_astronomy.csv
13a45bed754ffe8f0e7f0348e69085c648a1ae0e91aa76cd5e6906fa45f7ec8e  routellm/mmlu/responses/mmlu_business_ethics.csv
5ea37709bfbad4df7a3fcbd8973413f7645a2adfef9772490f09d3f293ffe882  routellm/mmlu/responses/mmlu_clinical_knowledge.csv
c38620db5b9f6f1b0fde7bc8d8299b8e2305c4dacbb1567c904cd51618d627d5  routellm/mmlu/responses/mmlu_college_biology.csv
78ca6ba22ff25c8fdb93e1c6e84384e72522106c73ed5ee9fed620928ce395ce  routellm/mmlu/responses/mmlu_college_chemistry.csv
8a7b2a87a36b428c9d1a8f12ce3b01cf7920f0a7146d6f8a111da6749c332da3  routellm/mmlu/responses/mmlu_college_computer_science.csv
3f22f42779c48509c66e21d7d1fc3054eb0256c1b5b772bb6d9a3af1b253238b  routellm/mmlu/responses/mmlu_college_mathematics.csv
63d71d29e630fc6538e1705a51f4267bdbbdea0fa7ee85056ad7d6f8f10d3987  routellm/mmlu/responses/mmlu_college_medicine.csv
8162a0660d4ef7b963088e0a176d4b8630096127a0d0b5ed003f649cbad34455  routellm/mmlu/responses/mmlu_college_physics.csv
26446faf7460f03aa2d82a05c170faedb5cbf6d7bdda133b1146229107097d1c  routellm/mmlu/responses/mmlu_computer_security.csv
ea40473704b64cb60d564ffe91962848ec6fc3106b40089d08cebc98d5c7226d  routellm/mmlu/responses/mmlu_conceptual_physics.csv
927b4d877cec8697f3b8378bb7e7c7a279babaea2edacf350ea50908701907b9  routellm/mmlu/responses/mmlu_econometrics.csv
de2fb7c2e9addb4808f5b3f7c79efbfdfb86216235bb7dc28ab70bc53664f11e  routellm/mmlu/responses/mmlu_electrical_engineering.csv
88697ff0d238b5852c7a959379dc8b0b45c3554ed4b2ee7cbb7ffc34e06c7cf5  routellm/mmlu/responses/mmlu_elementary_mathematics.csv
094a462626468569ad595c03e66e73a299188cfa175248b499dba94e005506e6  routellm/mmlu/responses/mmlu_formal_logic.csv
6f75ddf7d6eb94ac0471529a85ff7f8b738c9f4055185b3114eed26393222d06  routellm/mmlu/responses/mmlu_global_facts.csv
444982fc2c750ab8cbc6942a0c0267040aa5023c1d2364247d20e2a28b4e325d  routellm/mmlu/responses/mmlu_high_school_biology.csv
ae5e62479365dcd056354c568f60da65b1df9f1de57dda3953a8e9933a6f12e4  routellm/mmlu/responses/mmlu_high_school_chemistry.csv
d156d9448a6e899f7f976a0afac148a928bf709a2baa573f0c407854a25bdb11  routellm/mmlu/responses/mmlu_high_school_computer_science.csv
c64afd6c5e30ef0cd5c10beab961ac05e7ba55d6b48cd93bf6ad0a45f90515bf  routellm/mmlu/responses/mmlu_high_school_european_history.csv
21b12044be4a6b9813880ad46372006dc0846e7f85fa46883bb73dbd90fba30a  routellm/mmlu/responses/mmlu_high_school_geography.csv
2791689acc61775a5857f4f3a5fd248d30552553c3a96f6ee63dc28285437259  routellm/mmlu/responses/mmlu_high_school_government_and_politics.csv
73b931a2a06ba74a5c9f1e005d45a25004e36be5d075d98f8ebcf89b3fda367b  routellm/mmlu/responses/mmlu_high_school_macroeconomics.csv
151b2a736981b7b20f3bad99de6a573e4692d68cf87020e943b601b70bb37390  routellm/mmlu/responses/mmlu_high_school_mathematics.csv
f7ea151efb4c9d96374db3358ac7e97e4d8349e26715f61dac98bc9d854ca046  routellm/mmlu/responses/mmlu_high_school_microeconomics.csv
1e2103fc8f7f1cfac7c00952b26db2418acab092486af78a9c51c551ae2ff9be  routellm/mmlu/responses/mmlu_high_school_physics.csv
d11604f2c7a2425258d5b9a285ef71737c011cc3a91456890f088a0ee4c55212  routellm/mmlu/responses/mmlu_high_school_psychology.csv
de6642bcd93d7a80e7bbd73d987a44d67b6e537b47749ddc518122fd582703bb  routellm/mmlu/responses/mmlu_high_school_statistics.csv
a71186d143363e203b2f216d78c9d21f10cca51de455ef176c9c01b77f546592  routellm/mmlu/responses/mmlu_high_school_us_history.csv
e9c52305977dc37b3e48eee727ad933b2d24d96ca3a06aec865a8900b1e87d2c  routellm/mmlu/responses/mmlu_high_school_world_history.csv
0d6c4109b4f61f138781ce50bbc60ff2e90dd451694d3926a088be5a5bd88aa8  routellm/mmlu/responses/mmlu_human_aging.csv
20916037e3012731883f926738daf47ed994f74ba25518fdb7f0df72a81143fd  routellm/mmlu/responses/mmlu_human_sexuality.csv
e389b886e3e1570bfdf798f6f56dc3c675350200c5baf0f41a0766294ff312eb  routellm/mmlu/responses/mmlu_international_law.csv
7e94c3849231be7b2654db11e44ee51531f8474e300fe83b1e05438df3038f0b  routellm/mmlu/responses/mmlu_jurisprudence.csv
fa2d94cd2e901621aaebd0a7e64560b5dbbe4249086795398533eed2561c0c1e  routellm/mmlu/responses/mmlu_logical_fallacies.csv
9301e928481be6e20b3541e1ac112b25a4a71338b21592978c0d219cb6240020  routellm/mmlu/responses/mmlu_machine_learning.csv
a00b12ab0265918f9335a85ce02303ddaea0491a7b099f88a05986eeac4aa4fb  routellm/mmlu/responses/mmlu_management.csv
3ee0b6560ebe0eb4f14ce63227d6f6ec8cb8b6e57e345eae9460be742672f36c  routellm/mmlu/responses/mmlu_marketing.csv
7c830f87dcbc0029137b3400cf361931831cb57dda55717a64e72ad74663d5df  routellm/mmlu/responses/mmlu_medical_genetics.csv
c2e1a5b0394a8429f55b6340e1865166ac17ec6b3571523005e413b3b2843b9f  routellm/mmlu/responses/mmlu_miscellaneous.csv
8b2b973b1987b724f39fd10970604d7f1150747a0db9ce766c193bd978c9d0c4  routellm/mmlu/responses/mmlu_moral_disputes.csv
78ded6fdca1cd0d5c4ed16a5ed4aebf2e98401b9c00b20e565e6cce5bf1153c7  routellm/mmlu/responses/mmlu_moral_scenarios.csv
ba197fbf4b6c5034414d66ea7747211056cf5f709f83d919d2d856245720cc51  routellm/mmlu/responses/mmlu_nutrition.csv
2f30ad01ab529ddea00dff4ff87e950fef838a78754c94f6884120628a7d0591  routellm/mmlu/responses/mmlu_philosophy.csv
afc816d4b78814627bae9b8c3730a9bb72bd958ff6259fd36c981777cfe83753  routellm/mmlu/responses/mmlu_prehistory.csv
fe5ee976a625832ecf624086103619523d063a3a728c60681b6a53c55b638a37  routellm/mmlu/responses/mmlu_professional_accounting.csv
60a4308df4f481ff8840590b29061835207b50a9f06a9fc68baf12b114f7e2e0  routellm/mmlu/responses/mmlu_professional_law.csv
6fda30b523e4e2ce8b1f18e871a0722190a4ac81ec377a75fbba4a63e0b510f7  routellm/mmlu/responses/mmlu_professional_medicine.csv
bfb3a7fb653e2b195d3fa258894af64d3f1c3dbd4059c1e0feb728131d20f9b3  routellm/mmlu/responses/mmlu_professional_psychology.csv
2196279ea2535fbda62509c3156209b764459a3d515a9ba84b69cf16bb4c27d0  routellm/mmlu/responses/mmlu_public_relations.csv
1354bb17649f95b7dd8ef3179e22329abe6bef84e639d63370ae9e80b2509905  routellm/mmlu/responses/mmlu_security_studies.csv
903ba6259b13f2877607abef22b7b19cb575104268d4ced615e3129d3caafb47  routellm/mmlu/responses/mmlu_sociology.csv
f1c1d266753a3f161e2018c16c6948fb78c7f5e506a10b4fc0441049962a6c69  routellm/mmlu/responses/mmlu_us_foreign_policy.csv
d525f1ba88982befd42140e73822fe940e73db82bbdcdd569d95f26c3dd41e68  routellm/mmlu/responses/mmlu_virology.csv
49dc00d44e069ecaac6a7f7e2188f4a8bce5df56ee91bb15f55519d07f64af8e  routellm/mmlu/responses/mmlu_world_religions.csv
MANIFEST
}

while read -r sha rel; do
	fetch "$rel" "$sha"
done < <(manifest)

(cd research && uv run python -m triage_research.make_eval_sets convert --raw "$DATA_DIR/raw" --out "$DATA_DIR")
echo "eval data ready in $DATA_DIR"
