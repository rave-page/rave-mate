const std = @import("std");

// Static webui render lib consumed by Go via cgo (internal/zigui). Build:
//   zig build -Drelease [-Dtarget=x86_64-windows-gnu]
// Artifact: zig-out/lib/libraveui.a (gnu targets — links with mingw gcc).
pub fn build(b: *std.Build) void {
    const target = b.standardTargetOptions(.{});
    const optimize = b.standardOptimizeOption(.{ .preferred_optimize_mode = .ReleaseFast });

    const mod = b.createModule(.{
        .root_source_file = b.path("src/root.zig"),
        .target = target,
        .optimize = optimize,
        .link_libc = true, // c_allocator; mingw runtime satisfies at final Go link
    });

    const lib = b.addLibrary(.{
        .name = "raveui",
        .linkage = .static,
        .root_module = mod,
    });
    // No bundled compiler-rt: mingw libgcc/quadmath provide the intrinsics at the Go
    // link, and bundling in >1 Zig lib duplicates weak symbols (combined-tag link fails).
    lib.bundle_compiler_rt = false;
    b.installArtifact(lib);

    const tests = b.addTest(.{ .root_module = mod });
    const run_tests = b.addRunArtifact(tests);
    const test_step = b.step("test", "Run unit tests");
    test_step.dependOn(&run_tests.step);

    // rave-shell: PSH1 window child exe (ZIG_MIGRATION B6) - Win32 window + WebView2 behind the
    // B5 procShell protocol. Windows-only (WebView2); opt-in via daemon features.ui.shellImpl.
    if (target.result.os.tag == .windows) {
        const shell_mod = b.createModule(.{
            .root_source_file = b.path("src/shell/main.zig"),
            .target = target,
            .optimize = optimize,
        });
        const shell_exe = b.addExecutable(.{
            .name = "rave-shell",
            .root_module = shell_mod,
        });
        shell_exe.subsystem = .Windows; // GUI subsystem: no console; stdio pipes still flow
        b.installArtifact(shell_exe);

        const shell_tests = b.addTest(.{ .root_module = shell_mod });
        const run_shell_tests = b.addRunArtifact(shell_tests);
        test_step.dependOn(&run_shell_tests.step);
    }
}
