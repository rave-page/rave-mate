/*{
  "DESCRIPTION": "Brightness / contrast / saturation (0.5 = neutral)",
  "CREDIT": "rave-mate (MIT)",
  "ISFVSN": "2",
  "CATEGORIES": ["Color Adjustment"],
  "INPUTS": [
    {"NAME": "inputImage", "TYPE": "image"},
    {"NAME": "brightness", "TYPE": "float", "DEFAULT": 0.5},
    {"NAME": "contrast", "TYPE": "float", "DEFAULT": 0.5},
    {"NAME": "saturation", "TYPE": "float", "DEFAULT": 0.5}
  ]
}*/
void main() {
  vec4 c = IMG_THIS_PIXEL(inputImage);
  c.rgb += (brightness - 0.5);
  c.rgb = (c.rgb - 0.5) * (contrast * 2.0) + 0.5;
  float l = dot(c.rgb, vec3(0.2126, 0.7152, 0.0722));
  c.rgb = mix(vec3(l), c.rgb, saturation * 2.0);
  gl_FragColor = vec4(clamp(c.rgb, 0.0, 1.0), c.a);
}
