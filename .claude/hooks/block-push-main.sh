#!/bin/sh
# PreToolUse hook (Bash): refuse `git push` that targets main.
# Work lands on main only through a GitHub PR + merge (see CLAUDE.md > Workflow).
# Reads the tool call JSON on stdin; exit 2 blocks the call and shows stderr to the agent.
cmd=$(node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{process.stdout.write(JSON.parse(s).tool_input.command||"")}catch{}})')
case "$cmd" in
  *"git push"*)
    # explicit main target
    if printf '%s' "$cmd" | grep -Eq 'git push[^;&|]*(\s|:)(main|origin/main)(\s|$)'; then
      echo "Blocked: pushing to main directly. Push the feature branch and open a PR." >&2
      exit 2
    fi
    # bare `git push` while checked out on main
    if ! printf '%s' "$cmd" | grep -Eq 'git push\s+\S+\s+\S+'; then
      if [ "$(git rev-parse --abbrev-ref HEAD 2>/dev/null)" = "main" ]; then
        echo "Blocked: you are on main. Create a branch and open a PR instead." >&2
        exit 2
      fi
    fi
    ;;
esac
exit 0
