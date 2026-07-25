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

    const tests = b.addTest(.{ .root_module = mod });
    const run_tests = b.addRunArtifact(tests);
    b.step("test", "Run unit tests").dependOn(&run_tests.step);
}
