/*
 * Aztec's Linux archive is compiled against glibc headers, where res_init is
 * redirected to the private symbol __res_init. musl exports the public POSIX
 * name only, so provide the narrow compatibility alias in the musl archive.
 */
extern int res_init(void);

int __res_init(void)
{
    return res_init();
}
