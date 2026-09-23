#!/usr/bin/env bash

# With CDPATH set, cd bin can change into a bin directory outside the repo.
unset CDPATH

print_color() {
    case $1 in
        red) echo -e "\033[31m$2\033[0m" ;;
        yellow) echo -e "\033[33m$2\033[0m" ;;
    esac
}

error() {
    print_color red "Error: $*" >&2
}

fatal() {
    error "$*"
    exit 1
}

warning() {
    print_color yellow "Warning: $*" >&2
}

set_source_and_root_dir() {
    source_dir="$(cd -P "$(dirname "$0")" > /dev/null 2>&1 && pwd)"
    root_dir=$(cd "$source_dir" && cd ../ && pwd)
    cd "$root_dir" || fatal "Could not change to root directory: $root_dir"
}

command_exists() {
    command -v "$1" > /dev/null 2>&1
}

# BSD sed does not support \? in a basic regex. The -E flag makes ? work in BSD
# and GNU sed alike.
# set_source_and_root_dir has already changed directory, where a relative $0
# does not resolve. This reads the script through source_dir instead.
show_help() {
    sed -n -E 's/^#\/ ?//p' "$source_dir/$(basename "$0")"
}

parse_common_args() {
    for arg in "$@"; do
        case "$arg" in
            -h | --help)
                show_help
                exit 0
                ;;
        esac
    done
}
