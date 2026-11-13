const std = @import("std");

// zigmedia — native realtime media transport. Static lib consumed by the encode shims/children via
// the C ABI (include/ravemedia.h). Build:
//   zig build -Doptimize=ReleaseFast [-Dtarget=x86_64-linux-gnu|x86_64-windows-gnu]
// Artifact: zig-out/lib/libravemedia.a
pub fn build(b: *std.Build) void {
    const target = b.standardTargetOptions(.{});
    const optimize = b.standardOptimizeOption(.{ .preferred_optimize_mode = .ReleaseFast });

    const mod = b.createModule(.{
        .root_source_file = b.path("src/root.zig"),
        .target = target,
        .optimize = optimize,
        .link_libc = true, // c_allocator + the shim's final link
    });

    // Chaos/e2e numbers go to stderr only on request: the build runner flags ANY test stderr output with a
    // "failed command" line even on success, and a gate must not print a failure while passing.
    const opts = b.addOptions();
    opts.addOption(bool, "chaos_report", b.option(bool, "chaos-report", "print chaos/e2e acceptance numbers") orelse false);
    opts.addOption(i64, "soak_seconds", b.option(i64, "soak-seconds", "transport soak duration (acceptance: 600)") orelse 5);
    mod.addOptions("build_options", opts);

    if (target.result.os.tag == .windows) mod.linkSystemLibrary("ws2_32", .{}); // Winsock (sock.zig)

    const lib = b.addLibrary(.{
        .name = "ravemedia",
        .linkage = .static,
        .root_module = mod,
    });
    lib.bundle_compiler_rt = false; // mingw/glibc provide intrinsics at the shim's link
    b.installArtifact(lib);

    const tests = b.addTest(.{ .root_module = mod });
    const run_tests = b.addRunArtifact(tests);
    const test_step = b.step("test", "Run wire-core goldens + unit tests");
    test_step.dependOn(&run_tests.step);
}
