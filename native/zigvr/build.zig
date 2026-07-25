const std = @import("std");

// VR-overlay raster lib consumed by Go via cgo (internal/zigvr). Build:
//   zig build -Drelease [-Dtarget=x86_64-windows-gnu]
// Artifact: zig-out/lib/libravevr.a (gnu targets — links with mingw gcc).
pub fn build(b: *std.Build) void {
    const target = b.standardTargetOptions(.{});
    const optimize = b.standardOptimizeOption(.{ .preferred_optimize_mode = .ReleaseFast });

    const mod = b.createModule(.{
        .root_source_file = b.path("src/root.zig"),
        .target = target,
        .optimize = optimize,
        .link_libc = true, // mingw runtime satisfied at final Go link
    });

    const lib = b.addLibrary(.{
        .name = "ravevr",
        .linkage = .static,
        .root_module = mod,
    });
    // No bundled compiler-rt: mingw libgcc/quadmath provide the intrinsics at the Go
    // link, and bundling in >1 Zig lib duplicates weak symbols (combined-tag link fails).
    lib.bundle_compiler_rt = false;
    b.installArtifact(lib);

    const tests = b.addTest(.{ .root_module = mod });
    const run_tests = b.addRunArtifact(tests);
    b.step("test", "Run unit tests").dependOn(&run_tests.step);
}
