# shellcheck shell=bash
# lib/diff.sh — reading a unified diff, for the two questions revloop asks of one.
#
# Both questions are about the same thing: which line of which file a given line
# of the diff is. The review leg needs that answer written down, because a model
# that has to derive it counts lines under a `@@` header and sometimes counts
# wrong. The orchestrator needs it again before posting, because GitHub accepts a
# comment only on a line the diff actually shows, and a finding one line outside
# a hunk is refused and falls out of the thread it belongs in.
#
# So there is one state machine here and two ways out of it, chosen by `mode`.
# Splitting them into two parsers would mean two places to keep the rule that a
# bare empty line inside a hunk is context, and the first one to drift silently
# renumbers everything after it.
#
# No GitHub call, no credential, no network. That is what makes the whole thing
# testable against a fixture, which is where the pull-request-14 case lives.

# The default snap distance, in lines. Three is the number of context lines git
# puts either side of a change, so it is exactly the margin a miscount lands in:
# the reviewer read the right hunk and fell off its end. Past that the reviewer
# meant somewhere else, and moving the comment would anchor it to code the
# finding never mentions — worse than not anchoring it at all.
REVLOOP_DIFF_SNAP=3

# _diff_parse <mode> <file> [path] [side] [line] [bound]
#
# mode `number`: the diff back with an old/new line-number gutter.
# mode `anchor`: the line to anchor to, or nothing.
_diff_parse() {
  local mode="$1" file="$2" want_path="${3:-}" want_side="${4:-}" want_line="${5:-0}" bound="${6:-0}"
  [[ -s "$file" ]] || return 0
  # LC_ALL=C so every substr, length and sprintf below counts bytes rather than
  # characters. A path git escaped as octal has to be rebuilt one byte at a
  # time — under a UTF-8 locale `sprintf("%c", 195)` yields a two-byte
  # character instead of the byte the path is made of.
  #
  # The path arrives through the environment rather than through `-v`, because
  # `-v` runs its value through awk's own escape processing: a path containing a
  # backslash would be decoded on the way in and then fail to match the path in
  # the diff, or match one it is not. ENVIRON hands the bytes over untouched.
  REVLOOP_DIFF_PATH="$want_path" REVLOOP_DIFF_SIDE="$want_side" \
  LC_ALL=C awk -v mode="$mode" -v want_line="$want_line" -v bound="$bound" '
    BEGIN { want_path = ENVIRON["REVLOOP_DIFF_PATH"]; want_side = ENVIRON["REVLOOP_DIFF_SIDE"] }
    # Undo git C-style quoting: the whole path wrapped in double quotes, with
    # \\ and \" escaped, the usual control-character escapes, and \### octal for
    # every byte outside printable ASCII. GitHub anchors a comment to the raw
    # path, so the quoting is decoded rather than compared against.
    function unquote(s,   out, i, e, n, j) {
      if (substr(s, 1, 1) != "\"") return s
      s = substr(s, 2, length(s) - 2)
      out = ""
      for (i = 1; i <= length(s); i++) {
        if (substr(s, i, 1) != "\\") { out = out substr(s, i, 1); continue }
        e = substr(s, i + 1, 1)
        if (e >= "0" && e <= "7") {
          n = 0
          for (j = 0; j < 3 && substr(s, i + 1, 1) >= "0" && substr(s, i + 1, 1) <= "7"; j++) {
            n = n * 8 + (substr(s, i + 1, 1) + 0); i++
          }
          out = out sprintf("%c", n)
          continue
        }
        i++
        if      (e == "a") out = out sprintf("%c", 7)
        else if (e == "b") out = out sprintf("%c", 8)
        else if (e == "t") out = out sprintf("%c", 9)
        else if (e == "n") out = out sprintf("%c", 10)
        else if (e == "v") out = out sprintf("%c", 11)
        else if (e == "f") out = out sprintf("%c", 12)
        else if (e == "r") out = out sprintf("%c", 13)
        else               out = out e
      }
      return out
    }
    # One path off a `---` or `+++` line, decoded and with its side prefix off.
    #
    # These carry one path each and the `diff --git` header carries two with no
    # separator that cannot appear inside a name, so `diff --git a/my file.md
    # b/my file.md` is genuinely ambiguous and reading whitespace fields off it
    # makes `my` and `file.md` out of one name. A trailing tab is git marking a
    # name it could not otherwise delimit, and never part of the name — a real
    # tab inside one is escaped, so the quoted form has no literal tab in it.
    function header_path(rest, prefix,   p) {
      sub(/\t.*$/, "", rest)
      p = unquote(rest)
      if (p == "/dev/null") return ""
      sub("^" prefix, "", p)
      return p
    }
    function keep(kind, o, n, raw) {
      if (mode != "number") return
      rows++; k[rows] = kind; ro[rows] = o; rn[rows] = n; rr[rows] = raw
      if (o != "-" && o + 0 > widest) widest = o + 0
      if (n != "-" && n + 0 > widest) widest = n + 0
    }
    # A candidate is a line this file shows on the side being asked about.
    function offer(o, n,    v) {
      if (mode != "anchor") return
      if (path != want_path && bpath != want_path) return
      v = (want_side == "LEFT") ? o : n
      if (v == "-") return
      d = (v < want_line) ? want_line - v : v - want_line
      # Ties go to the earlier line. A finding names something at or after the
      # number the reviewer gave more often than before it, so on a tie the
      # lower number is the better guess at what they were looking at.
      if (best == "" || d < bestd || (d == bestd && v < best)) { best = v; bestd = d }
    }

    # The header only clears the previous file. Its own paths are read off the
    # two lines below it, which a file with hunks always has.
    /^diff --git / {
      path = ""; bpath = ""
      in_hunk = 0; keep("H", "-", "-", $0); next
    }
    # Hunk counts are omitted when they are 1, so read only up to the comma.
    /^@@/ {
      o = substr($2, 2); sub(/,.*/, "", o)
      n = substr($3, 2); sub(/,.*/, "", n)
      oldno = o + 0; newno = n + 0
      in_hunk = 1; keep("H", "-", "-", $0); next
    }
    # `--- a/x` and `+++ b/x` are matched only outside a hunk, so a deleted line
    # whose own text starts with `--` is still read as a deletion.
    !in_hunk && /^--- / { path  = header_path(substr($0, 5), "a/"); keep("H", "-", "-", $0); next }
    !in_hunk && /^\+\+\+ / { bpath = header_path(substr($0, 5), "b/"); keep("H", "-", "-", $0); next }
    !in_hunk { keep("H", "-", "-", $0); next }
    # "\ No newline at end of file" annotates the line above and is not one.
    /^\\/ { keep("H", "-", "-", $0); next }

    substr($0, 1, 1) == "+" { offer("-", newno); keep("B", "-", newno, $0); newno++; next }
    substr($0, 1, 1) == "-" { offer(oldno, "-"); keep("B", oldno, "-", $0); oldno++; next }
    # Context, including a blank line that lost its leading space in transit.
    { offer(oldno, newno); keep("B", oldno, newno, $0); oldno++; newno++ }

    END {
      if (mode == "anchor") {
        if (best != "" && bestd <= bound) print best
        exit
      }
      w = length(widest ""); if (w < 4) w = 4
      fmt = "%" w "s %" w "s |%s\n"
      for (i = 1; i <= rows; i++) {
        if (k[i] == "H") print rr[i]; else printf fmt, ro[i], rn[i], rr[i]
      }
    }
  ' "$file"
}

# diff_number <diff_file>
#
# The diff with every hunk line prefixed by its old and its new line number, a
# dash standing in for the side that does not have one. Headers pass through
# bare so the gutter reads as a gutter rather than as part of the diff.
#
# This is what the review leg is given instead of the raw diff. The number it
# must put in a finding is now on the line it is looking at, and the dash says
# which side that line can take a comment on without being told separately.
diff_number() { _diff_parse number "$1"; }

# diff_anchor <diff_file> <path> <side> <line> [bound]
#
# The line to actually post on: the one asked for when the diff shows it, the
# nearest one it does show when that is within `bound`, and nothing at all when
# the file is absent from the diff or the miss is too wide to repair.
#
# Nothing is a real answer, not a failure. It means the finding has to go
# somewhere other than a line, and the caller has to say so.
diff_anchor() {
  local file="$1" path="$2" side="$3" line="$4" bound="${5:-$REVLOOP_DIFF_SNAP}"
  _diff_parse anchor "$file" "$path" "$side" "$line" "$bound"
}
