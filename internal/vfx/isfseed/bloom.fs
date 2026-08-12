/*{
  "DESCRIPTION": "Multi-pass bloom: bright-pass at half res, separable blur, additive composite",
  "CREDIT": "rave-mate (MIT)",
  "ISFVSN": "2",
  "CATEGORIES": ["Stylize"],
  "INPUTS": [
    {"NAME": "inputImage", "TYPE": "image"},
    {"NAME": "threshold", "TYPE": "float", "DEFAULT": 0.6},
    {"NAME": "intensity", "TYPE": "float", "DEFAULT": 0.7},
    {"NAME": "radius", "TYPE": "float", "DEFAULT": 0.5}
  ],
  "PASSES": [
    {"TARGET": "bright", "WIDTH": "$WIDTH/2.0", "HEIGHT": "$HEIGHT/2.0"},
    {"TARGET": "blurA", "WIDTH": "$WIDTH/2.0", "HEIGHT": "$HEIGHT/2.0"},
    {"TARGET": "blurB", "WIDTH": "$WIDTH/2.0", "HEIGHT": "$HEIGHT/2.0"},
    {}
  ]
}*/
vec4 blur5(sampler2D img, vec2 uv, vec2 dir) {
  vec2 px = dir * (1.0 + radius * 3.0) / RENDERSIZE;
  vec4 s = IMG_NORM_PIXEL(img, uv) * 0.375;
  s += IMG_NORM_PIXEL(img, uv + px) * 0.25;
  s += IMG_NORM_PIXEL(img, uv - px) * 0.25;
  s += IMG_NORM_PIXEL(img, uv + 2.0 * px) * 0.0625;
  s += IMG_NORM_PIXEL(img, uv - 2.0 * px) * 0.0625;
  return s;
}
void main() {
  vec2 uv = isf_FragNormCoord;
  if (PASSINDEX == 0) {
    vec4 c = IMG_NORM_PIXEL(inputImage, uv);
    float luma = dot(c.rgb, vec3(0.2126, 0.7152, 0.0722));
    gl_FragColor = vec4(c.rgb * smoothstep(threshold, threshold + 0.2, luma), 1.0);
  } else if (PASSINDEX == 1) {
    gl_FragColor = blur5(bright, uv, vec2(1.0, 0.0));
  } else if (PASSINDEX == 2) {
    gl_FragColor = blur5(blurA, uv, vec2(0.0, 1.0));
  } else {
    vec4 c = IMG_NORM_PIXEL(inputImage, uv);
    vec4 b = IMG_NORM_PIXEL(blurB, uv);
    gl_FragColor = vec4(c.rgb + b.rgb * intensity, c.a);
  }
}
