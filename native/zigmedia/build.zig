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
