sprite_name() {
  printf 'satelle-%s\n' "$(printf '%s' "$1" | tr '_' '-')"
}
