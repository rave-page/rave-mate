/*{
  "DESCRIPTION": "Color tint; strength blends between original and tinted",
  "CREDIT": "rave-mate (MIT)",
  "ISFVSN": "2",
  "CATEGORIES": ["Color Effect"],
  "INPUTS": [
    {"NAME": "inputImage", "TYPE": "image"},
    {"NAME": "tint", "TYPE": "color", "DEFAULT": [1.0, 0.3, 0.6, 1.0]},
    {"NAME": "strength", "TYPE": "float", "DEFAULT": 0.5}
  ]
}*/
void main() {
  vec4 c = IMG_THIS_PIXEL(inputImage);
  float luma = dot(c.rgb, vec3(0.2126, 0.7152, 0.0722));
  vec3 tinted = mix(c.rgb * tint.rgb, tint.rgb * luma, 0.5);
  gl_FragColor = vec4(mix(c.rgb, tinted, strength), c.a);
}
