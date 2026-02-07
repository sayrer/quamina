#!/bin/bash
#
# Lazy DFA Performance Demo
#
# This script demonstrates the lazy DFA optimization for shell-style
# and wildcard patterns. See lazy_dfa_design.md for detailed explanation.
#

set -e

cd "$(dirname "$0")/.."

echo "=============================================="
echo "Lazy DFA Performance Demo"
echo "=============================================="
echo ""
echo "For detailed design documentation, see:"
echo "  lazy_dfa_design.md"
echo ""

# Colors for output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}1. Running shellstyle benchmark...${NC}"
echo ""
go test -bench=BenchmarkShellstyleMultiMatch -benchmem -run=^$ . 2>/dev/null | grep -E "(Benchmark|ns/op)"
echo ""

echo -e "${BLUE}2. Running 8259 example benchmark...${NC}"
echo ""
go test -bench=Benchmark8259Example -benchmem -run=^$ . 2>/dev/null | grep -E "(Benchmark|ns/op)"
echo ""

echo -e "${BLUE}3. Running lazy DFA state analysis tests...${NC}"
echo ""
echo "These tests show how pattern complexity and input diversity"
echo "affect DFA state counts and cache hit rates."
echo ""

go test -v -run 'TestLazyDFAStateStats' . 2>/dev/null | grep -E "(RUN|Pattern:|Caches:|Hits:)"
echo ""

echo -e "${BLUE}4. Testing state explosion protection...${NC}"
echo ""
echo "This test adds 27 overlapping patterns and sends diverse inputs"
echo "to trigger the 1000-state safety limit."
echo ""

go test -v -run 'TestLazyDFATryToExplode' . 2>/dev/null | grep -E "(RUN|After|HIT|Final)"
echo ""

echo -e "${BLUE}5. Memory analysis...${NC}"
echo ""
go test -v -run 'TestLazyDFAMemoryPerState' . 2>/dev/null | grep -E "(RUN|lazyDFAState|Transitions|Max memory)"
echo ""

echo -e "${GREEN}=============================================="
echo "Summary"
echo "==============================================${NC}"
echo ""
echo "Key findings:"
echo "  - Single patterns: 5-12 DFA states, 99%+ hit rate"
echo "  - Multiple overlapping patterns: 40-160 states"
echo "  - State limit (1000) protects against pathological cases"
echo "  - Memory: ~2KB per state, max ~2MB per cache"
echo ""
echo "The lazy DFA provides near-DFA speed for hot paths while"
echo "gracefully falling back to NFA traversal when needed."
echo ""
echo "See lazy_dfa_design.md for full documentation."
