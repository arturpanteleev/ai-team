#!/usr/bin/env bash
# Per-package coverage gate.
#
# Usage: scripts/coverage-gate.sh [coverprofile]
#
# Reads a Go coverprofile (as produced by `go test -coverprofile=...`),
# computes statement coverage per package from the profile itself and
# enforces floors:
#   - safety-critical packages must each be >= SAFETY_FLOOR
#   - aggregate coverage must be >= OVERALL_FLOOR
#
# Exits non-zero with a readable list of all packages below their floor.
set -euo pipefail

PROFILE="${1:-coverage.out}"

if [[ ! -f "${PROFILE}" ]]; then
    echo "coverage-gate: coverprofile not found: ${PROFILE}" >&2
    echo "usage: $0 [coverprofile]" >&2
    exit 1
fi

readonly OVERALL_FLOOR="60.0"

# Ratchet: floors start at the current actual coverage and must only go up.
# To raise a floor, edit scripts/coverage-floors.env in the same PR that
# improves the number; lowering a value is rejected by review.
SAFETY_PACKAGES=(pkg/approval pkg/checks pkg/delivery pkg/evidence pkg/pipeline pkg/safeio)
load_floors() {
    local script_dir
    script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    local env_file="${script_dir}/coverage-floors.env"
    if [[ ! -f "${env_file}" ]]; then
        echo "coverage-gate: ${env_file} not found" >&2
        exit 1
    fi
    # shellcheck disable=SC1091
    source "${env_file}"
}

floor_for() {
    case "$1" in
        pkg/approval)  echo "${PKG_APPROVAL_FLOOR:?}" ;;
        pkg/checks)    echo "${PKG_CHECKS_FLOOR:?}" ;;
        pkg/delivery)  echo "${PKG_DELIVERY_FLOOR:?}" ;;
        pkg/evidence)  echo "${PKG_EVIDENCE_FLOOR:?}" ;;
        pkg/pipeline)  echo "${PKG_PIPELINE_FLOOR:?}" ;;
        pkg/safeio)    echo "${PKG_SAFEIO_FLOOR:?}" ;;
        *) echo "";;
    esac
}
load_floors

MODULE="$(go list -m 2>/dev/null || true)"

# Per-package statement coverage computed directly from the coverprofile:
# every block contributes its statement count; blocks with count > 0 count
# as covered. This mirrors how `go tool cover -func` aggregates totals.
# Output: "<package> <coverage>" per line, "__total__" for the aggregate.
COVERAGE_DATA="$(mktemp)"
trap 'rm -f -- "${COVERAGE_DATA}"' EXIT

awk -v module="${MODULE}" '
    /^mode:/ { next }
    {
        split($1, a, ":")
        file = a[1]
        if (module != "") {
            n = length(module) + 1
            if (substr(file, 1, n) == module "/")
                file = substr(file, n + 1)
        }
        dir = file
        sub(/\/[^\/]*$/, "", dir)
        total[dir] += $2
        if ($3 + 0 > 0)
            covered[dir] += $2
    }
    END {
        grand_total = 0
        grand_covered = 0
        for (dir in total) {
            printf "%s %.2f\n", dir, 100.0 * covered[dir] / total[dir]
            grand_total += total[dir]
            grand_covered += covered[dir]
        }
        if (grand_total > 0)
            printf "%s %.2f\n", "__total__", 100.0 * grand_covered / grand_total
    }
' "${PROFILE}" | sort > "${COVERAGE_DATA}"

get_coverage() {
    awk -v pkg="$1" '$1 == pkg { print $2; found = 1 } END { exit found ? 0 : 1 }' "${COVERAGE_DATA}"
}

below_floor() {
    awk -v a="$1" -v f="$2" 'BEGIN { print (a + 0 < f) ? 1 : 0 }'
}

failures=""

for pkg in "${SAFETY_PACKAGES[@]}"; do
    if ! actual="$(get_coverage "${pkg}")"; then
        failures="${failures}  - ${pkg}: no coverage data in profile (floor $(floor_for "${pkg}")%)\n"
        continue
    fi
    echo "coverage-gate: ${pkg} ${actual}% (floor $(floor_for "${pkg}")%)"
    if [[ "$(below_floor "${actual}" "$(floor_for "${pkg}")")" == "1" ]]; then
        failures="${failures}  - ${pkg}: ${actual}% (floor $(floor_for "${pkg}")%)\n"
    fi
done

if ! total="$(get_coverage __total__)"; then
    failures="${failures}  - total: no coverage data in profile (floor ${OVERALL_FLOOR}%)\n"
else
    echo "coverage-gate: overall ${total}% (floor ${OVERALL_FLOOR}%)"
    if [[ "$(below_floor "${total}" "${OVERALL_FLOOR}")" == "1" ]]; then
        failures="${failures}  - total: ${total}% (floor ${OVERALL_FLOOR}%)\n"
    fi
fi

if [[ -n "${failures}" ]]; then
    {
        printf '\ncoverage-gate: packages below threshold:\n'
        printf '%b' "${failures}"
    } >&2
    exit 1
fi

echo "coverage-gate: OK"
