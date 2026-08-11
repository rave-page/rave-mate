const std = @import("std");

// rave-mate-vfx: open-standard video effects child (frei0r now, ISF next; Zig, no cgo).
// Spawned by the Go `vfx` worker: --list discovery, --frame preview, --pipe export.
//   zig build -Drelease [-Dtarget=x86_64-windows-gnu]
// Artifacts: zig-out/bin/rave-mate-vfx(.exe) + f0r_test_invert.{dll,so} (test-only plugin).
pub fn build(b: *std.Build) void {
    const target = b.standardTargetOptions(.{});
    const optimize = b.standardOptimizeOption(.{ .preferred_optimize_mode = .ReleaseFast });

    const mod = b.createModule(.{
        .root_source_file = b.path("src/main.zig"),
        .target = target,
        .optimize = optimize,
    });
    const exe = b.addExecutable(.{ .name = "rave-mate-vfx", .root_module = mod });
    exe.subsystem = .Console; // stdio data plane; parent hides the window
    b.installArtifact(exe);

    const tp_mod = b.createModule(.{
        .root_source_file = b.path("src/testplugin.zig"),
        .target = target,
        .optimize = optimize,
    });
    const tp = b.addLibrary(.{ .name = "f0r_test_invert", .root_module = tp_mod, .linkage = .dynamic });
    b.installArtifact(tp);

    const tests = b.addTest(.{ .root_module = mod });
    const run_tests = b.addRunArtifact(tests);
    const test_step = b.step("test", "Run unit tests");
    test_step.dependOn(&run_tests.step);
}
