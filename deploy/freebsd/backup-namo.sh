#!/bin/sh

set -eu

backup_dir="/var/backups/namo"
backup_stamp="$(date -u +%Y%m%dT%H%M%SZ)-$$"
pg_host="/tmp"
pg_port="5432"
pg_user="postgres"
clean_path="/bin:/usr/bin:/usr/local/bin:/usr/local/sbin"
globals_tmp=""
database_tmp=""
completion_tmp=""

cleanup()
{
	if [ -n "$globals_tmp" ] && [ -f "$globals_tmp" ]; then
		rm -f "$globals_tmp"
	fi
	if [ -n "$database_tmp" ] && [ -f "$database_tmp" ]; then
		rm -f "$database_tmp"
	fi
	if [ -n "$completion_tmp" ] && [ -f "$completion_tmp" ]; then
		rm -f "$completion_tmp"
	fi
}

trap cleanup 0 1 2 15

install -d -o postgres -g postgres -m 0700 "$backup_dir"
globals_tmp="$(mktemp "${backup_dir}/.globals-${backup_stamp}.XXXXXX")"
database_tmp="$(mktemp "${backup_dir}/.database-${backup_stamp}.XXXXXX")"
chown postgres:postgres "$globals_tmp" "$database_tmp"
chmod 0600 "$globals_tmp" "$database_tmp"

su -l postgres -c "umask 077; env -i PATH='$clean_path' PGHOST='$pg_host' PGPORT='$pg_port' PGUSER='$pg_user' pg_dumpall --globals-only > '$globals_tmp'"
test -s "$globals_tmp"

su -l postgres -c "umask 077; env -i PATH='$clean_path' PGHOST='$pg_host' PGPORT='$pg_port' PGUSER='$pg_user' pg_dump -Fc -f '$database_tmp' namo"
test -s "$database_tmp"
su -l postgres -c "env -i PATH='$clean_path' pg_restore --list '$database_tmp' >/dev/null"

globals_final="${backup_dir}/globals-${backup_stamp}.sql"
database_final="${backup_dir}/namo-${backup_stamp}.dump"
completion_final="${backup_dir}/complete-${backup_stamp}.manifest"
mv -f "$globals_tmp" "$globals_final"
globals_tmp=""
mv -f "$database_tmp" "$database_final"
database_tmp=""
completion_tmp="$(mktemp "${backup_dir}/.completion-${backup_stamp}.XXXXXX")"
printf '%s\n%s\n' "$globals_final" "$database_final" > "$completion_tmp"
chown postgres:postgres "$completion_tmp"
chmod 0600 "$completion_tmp"
mv -f "$completion_tmp" "$completion_final"
completion_tmp=""

trap - 0 1 2 15
printf '%s\n%s\n%s\n' "$globals_final" "$database_final" "$completion_final"
