//! Linux link fallback: GPUI's X11 backend links libxkbcommon-x11 and
//! libX11-xcb. When only their runtime libraries are installed (no -dev
//! package, so no unversioned .so), point the linker at versioned copies.
//! Installing libxkbcommon-x11-dev and libx11-xcb-dev makes this a no-op.

use std::{env, fs, os::unix::fs::symlink, path::PathBuf};

fn main() {
    println!("cargo:rerun-if-changed=build.rs");
    if env::var("CARGO_CFG_TARGET_OS").as_deref() != Ok("linux") {
        return;
    }
    let out = PathBuf::from(env::var("OUT_DIR").unwrap()).join("syslibs");
    let _ = fs::create_dir_all(&out);
    let dirs = ["/usr/lib/x86_64-linux-gnu", "/usr/lib64", "/usr/lib"];
    let libs = [("libxkbcommon-x11.so", "libxkbcommon-x11.so.0"), ("libX11-xcb.so", "libX11-xcb.so.1")];
    let mut linked = false;
    for (plain, versioned) in libs {
        if dirs.iter().any(|d| PathBuf::from(d).join(plain).exists()) {
            continue;
        }
        if let Some(src) = dirs.iter().map(|d| PathBuf::from(d).join(versioned)).find(|p| p.exists()) {
            let dst = out.join(plain);
            let _ = fs::remove_file(&dst);
            if symlink(&src, &dst).is_ok() {
                linked = true;
            }
        }
    }
    if linked {
        println!("cargo:rustc-link-search=native={}", out.display());
    }
}
