const std = @import("std");

// Static lib consumed by Go via cgo (internal/zignative). Build:
//   zig build -Doptimize=ReleaseFast [-Dtarget=x86_64-windows-gnu]
// Artifact: zig-out/lib/libravezig.a (gnu targets — links with mingw gcc).
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
        .name = "ravezig",
        .linkage = .static,
        .root_module = mod,
    });
    // Go links this lib with gcc, not zig — bundle compiler-rt so intrinsics resolve.
    lib.bundle_compiler_rt = true;
    b.installArtifact(lib);

    // rave-probe: standalone probe-worker executable (ZIG_MIGRATION P4) — same
    // newline-JSON stdio protocol as `rave-mate worker probe`, reuses the bands.zig
    // kernels. Installed to zig-out/bin/; opt-in via daemon features.workers.probeExe.
    const probe_mod = b.createModule(.{
        .root_source_file = b.path("src/probe_main.zig"),
        .target = target,
        .optimize = optimize,
    });
    const probe_exe = b.addExecutable(.{
        .name = "rave-probe",
        .root_module = probe_mod,
    });
    b.installArtifact(probe_exe);

    const tests = b.addTest(.{ .root_module = mod });
    const run_tests = b.addRunArtifact(tests);
    const probe_tests = b.addTest(.{ .root_module = probe_mod });
    const run_probe_tests = b.addRunArtifact(probe_tests);
    const test_step = b.step("test", "Run unit tests");
    test_step.dependOn(&run_tests.step);
    test_step.dependOn(&run_probe_tests.step);
}
